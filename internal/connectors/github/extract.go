package github

import (
	"context"
	"log/slog"
	"strings"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/gitcli"
	"github.com/kmcd/xray/internal/model"
)

// Extract is the entry point for a (repo, window) extraction. It builds a
// Provenance value, drives every sub-extractor, and returns the result.
//
// Errors at any single stage are logged and recorded under
// prov.Errors[<table>] but do not abort the rest of the run. Context
// cancellation does abort: PaginationComplete is flipped false on the way
// out so the manifest records the truncation.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	// Read the repo's .mailmap once per extraction. Failure modes:
	//   - file absent       -> zero-value Mailmap, prov.Flags["mailmap_applied"] = false
	//   - file present + ok -> populated Mailmap, flag = true iff non-empty
	//   - read or parse err -> zero-value Mailmap, flag = false, error recorded
	// canonicalCommitIdent + hashHandle still run on every identity so the
	// "h_<digits>" boundary contract holds whether or not aliases were
	// resolved.
	mm, err := c.git.ReadMailmap(ctx, repo.Clone)
	if err != nil {
		if prov.Errors["mailmap"] == "" {
			prov.Errors["mailmap"] = err.Error()
		}
		c.log.Warn("github: read mailmap",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		mm = &gitcli.Mailmap{}
	}
	prov.Flags["mailmap_applied"] = mm.Applied()

	// Insert the repos row first so foreign-key-ish joins downstream have
	// something to anchor on. Fetch repo metadata via REST; tolerate
	// individual field failures by leaving them empty.
	if err := c.insertRepoRow(ctx, repo, window, sink, &prov); err != nil {
		prov.Errors["repos"] = err.Error()
		c.log.Warn("github: insert repo row", slog.String("repo", repo.Slug), slog.String("error", err.Error()))
	}

	// Teams: emit the team -> repo mapping so the teams table is populated
	// even when the only connector in use is github.
	if repo.Team != "" {
		if err := sink.InsertTeamRepo(repo.Team, repo.Slug); err != nil {
			prov.Errors["teams"] = err.Error()
		}
	}

	// Languages
	if err := c.extractLanguages(ctx, repo, sink, &prov); err != nil {
		prov.Errors["repo_languages"] = err.Error()
	}

	// Branches + branch protection
	c.extractBranches(ctx, repo, sink, &prov)

	// Codeowners
	c.extractCodeowners(ctx, repo, sink, &prov)

	// Releases + deploys
	c.extractReleases(ctx, repo, window, sink, &prov)

	// Commits, commit_files, commit_coauthors (driven by git log)
	c.extractCommits(ctx, repo, window, sink, &prov, mm)

	// PRs + pr_commits + reviews + pr_comments + pr_review_requests + pr_labels
	c.extractPRs(ctx, repo, window, sink, &prov)

	// File metrics and harness artifacts. These are implemented by the M4
	// agent in the same package; forward calls are fine.
	fileMetrics(ctx, c, repo, sink, &prov)
	harnessArtifacts(ctx, c, repo, window, sink, &prov)

	if err := ctx.Err(); err != nil {
		prov.PaginationComplete = false
	}

	return prov
}

// insertRepoRow fetches repo metadata via REST and emits the repos row.
// HeadSHA, default branch, and team come from the connector.Repo struct
// (already resolved by the caller from the local clone); the rest is
// pulled from the API.
func (c *Connector) insertRepoRow(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) error {
	owner, name, ok := splitSlug(repo.Slug)
	row := model.Repo{
		Slug:          repo.Slug,
		DefaultBranch: repo.DefaultBranch,
		HeadSHA:       repo.HeadSHA,
		Team:          repo.Team,
	}
	if ok {
		r, _, err := c.rest.Repositories.Get(ctx, owner, name)
		if err == nil && r != nil {
			row.PrimaryLanguage = r.GetLanguage()
			if r.CreatedAt != nil {
				t := r.CreatedAt.UTC()
				row.CreatedAt = &t
			}
			row.IsFork = r.GetFork()
			row.IsArchived = r.GetArchived()
			row.Visibility = r.GetVisibility()
			if row.DefaultBranch == "" {
				row.DefaultBranch = r.GetDefaultBranch()
			}
		} else if err != nil {
			c.log.Warn("github: get repo metadata",
				slog.String("repo", repo.Slug),
				slog.String("error", err.Error()),
			)
		}

		// Contributor count via list endpoint (anon=true, per_page=1, walk Link).
		if n, err := c.countContributors(ctx, owner, name); err == nil {
			row.ContributorCount = n
		}
	}
	if err := sink.InsertRepo(row); err != nil {
		return err
	}
	prov.RowsReturned["repos"]++
	return nil
}

// countContributors returns the total number of contributors by paginating
// the contributors endpoint with per_page=1 and reading the rel="last"
// Link header. Best-effort; returns 0 on error.
func (c *Connector) countContributors(ctx context.Context, owner, name string) (int, error) {
	opts := &gh.ListContributorsOptions{
		Anon:        "true",
		ListOptions: gh.ListOptions{PerPage: 1},
	}
	contribs, resp, err := c.rest.Repositories.ListContributors(ctx, owner, name, opts)
	if err != nil {
		return 0, err
	}
	if resp != nil && resp.LastPage > 0 {
		return resp.LastPage, nil
	}
	return len(contribs), nil
}

// splitSlug splits "owner/repo" -> (owner, repo, true). Returns ok=false
// if the slug is malformed.
func splitSlug(slug string) (string, string, bool) {
	idx := strings.IndexByte(slug, '/')
	if idx <= 0 || idx == len(slug)-1 {
		return "", "", false
	}
	return slug[:idx], slug[idx+1:], true
}
