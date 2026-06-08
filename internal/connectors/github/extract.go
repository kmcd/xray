package github

import (
	"context"
	"log/slog"
	"strings"
	"sync"

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
//
// Stages are organised into three phases (see #71):
//
//  1. Sync prelude — mailmap + repo row + team mapping. Fast and feeds
//     downstream state.
//  2. Parallel block — two goroutines:
//     A) clone-bound: languages, branches, codeowners, releases, commits,
//     file_metrics, harness_artifacts. Writes to provA.
//     B) API-bound:   PRs (prefers prefetch cache when populated by
//     run.go's clone-phase prefetch goroutine). Writes to provB.
//  3. Sync postlude — merge provA + provB into prov.
//
// The store (sink) is already mutex-guarded for concurrent inserts, so the
// two goroutines write rows safely. Provenance fragments are disjoint by
// design (the goroutines own non-overlapping error/row contexts) so the
// merge is loss-less under the first-wins-per-context policy in
// (*Provenance).Merge.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	// --- Phase 1: sync prelude --------------------------------------------

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

	// --- Phase 2: parallel block ------------------------------------------

	provA := connector.NewProvenance(c.Name(), repo.Slug, window)
	provB := connector.NewProvenance(c.Name(), repo.Slug, window)
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: clone-bound stages.
	go func() {
		defer wg.Done()
		if err := c.extractLanguages(ctx, repo, sink, &provA); err != nil {
			provA.Errors["repo_languages"] = err.Error()
		}
		c.extractBranches(ctx, repo, sink, &provA)
		c.extractCodeowners(ctx, repo, sink, &provA)
		c.extractReleases(ctx, repo, window, sink, &provA)
		c.extractCommits(ctx, repo, window, sink, &provA, mm)
		fileMetrics(ctx, c, repo, sink, &provA)
		harnessArtifacts(ctx, c, repo, window, sink, &provA)
	}()

	// Goroutine B: API-bound PR stage. Uses prefetch cache when present.
	go func() {
		defer wg.Done()
		c.extractPRs(ctx, repo, window, sink, &provB)
	}()

	wg.Wait()

	// --- Phase 3: sync postlude -------------------------------------------

	prov.Merge(provA)
	prov.Merge(provB)

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
