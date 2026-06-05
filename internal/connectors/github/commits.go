package github

import (
	"context"
	"log/slog"
	"net/http"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractCommits drives git log over the local clone and emits commits,
// commit_files, and commit_coauthors rows. Signature verification and
// landed_via_pr are filled in via REST best-effort.
func (c *Connector) extractCommits(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	if repo.Clone == "" {
		// No clone -> no commits. Caller already recorded the clone failure.
		return
	}
	records, err := c.git.LogNumstat(ctx, repo.Clone, window.Start, window.End, repo.DefaultBranch)
	if err != nil {
		prov.Errors["commits"] = err.Error()
		c.log.Warn("github: git log failed",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		return
	}

	owner, name, slugOK := splitSlug(repo.Slug)
	// landedCache avoids re-asking the API for the same SHA across PR commit
	// fan-out; keys are commit SHAs, values are *bool (nil = unknown).
	landedCache := map[string]*bool{}

	for _, rec := range records {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}

		row := model.Commit{
			SHA:             rec.SHA,
			Repo:            repo.Slug,
			AuthorHandle:    rec.AuthorHandle,
			CommitterHandle: rec.CommitterHandle,
			AuthoredAt:      rec.AuthoredAt,
			CommittedAt:     rec.CommittedAt,
			MessageSubject:  rec.Subject,
			AuthorIsBot:     isBot(rec.AuthorHandle),
			CommitterIsBot:  isBot(rec.CommitterHandle),
			IsRevert:        parseIsRevert(rec.Subject, rec.Body),
			RevertsSHA:      parseRevertsSHA(rec.Body),
			HasHotfixMarker: parseHasHotfixMarker(rec.Body),
			IsMerge:         len(rec.ParentSHAs) > 1,
		}

		for _, f := range rec.Files {
			row.Additions += f.Additions
			row.Deletions += f.Deletions
			row.FilesChanged++
		}

		// Signature verified + landed_via_pr via REST. Skip silently on
		// per-commit failure; leave the pointer nil so the analyser reads
		// the field as unknown rather than false.
		if slugOK {
			if v := c.fetchSignatureVerified(ctx, owner, name, rec.SHA); v != nil {
				row.SignatureVerified = v
			}
			if v := c.fetchLandedViaPR(ctx, owner, name, rec.SHA, landedCache); v != nil {
				row.LandedViaPR = v
			}
		}

		if err := sink.InsertCommit(row); err != nil {
			if prov.Errors["commits"] == "" {
				prov.Errors["commits"] = err.Error()
			}
			c.log.Warn("github: insert commit", slog.String("sha", rec.SHA), slog.String("error", err.Error()))
		} else {
			prov.RowsReturned["commits"]++
		}

		// Per-file rows.
		for _, f := range rec.Files {
			cf := model.CommitFile{
				CommitSHA:  rec.SHA,
				Repo:       repo.Slug,
				Path:       f.Path,
				Additions:  f.Additions,
				Deletions:  f.Deletions,
				ChangeType: f.ChangeType,
				PrevPath:   f.PrevPath,
			}
			if err := sink.InsertCommitFile(cf); err != nil {
				if prov.Errors["commit_files"] == "" {
					prov.Errors["commit_files"] = err.Error()
				}
			} else {
				prov.RowsReturned["commit_files"]++
			}
		}

		// Coauthor rows.
		for _, ca := range trailerCoauthors(rec, repo.Slug) {
			if err := sink.InsertCommitCoauthor(ca); err != nil {
				if prov.Errors["commit_coauthors"] == "" {
					prov.Errors["commit_coauthors"] = err.Error()
				}
			} else {
				prov.RowsReturned["commit_coauthors"]++
			}
		}
		if ca, ok := committerDistinctCoauthor(rec, repo.Slug); ok {
			if err := sink.InsertCommitCoauthor(ca); err != nil {
				if prov.Errors["commit_coauthors"] == "" {
					prov.Errors["commit_coauthors"] = err.Error()
				}
			} else {
				prov.RowsReturned["commit_coauthors"]++
			}
		}
	}
}

// fetchSignatureVerified asks the REST API for the verification flag on a
// single commit. Returns nil on any error or when verification info is
// absent.
func (c *Connector) fetchSignatureVerified(ctx context.Context, owner, name, sha string) *bool {
	// Use Repositories.GetCommit which returns RepositoryCommit including
	// the inner Commit with Verification. Avoid Git.GetCommit which keys on
	// tree SHA in some shapes; the repository endpoint is the friendly one.
	rc, resp, err := c.rest.Repositories.GetCommit(ctx, owner, name, sha, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return nil
	}
	if rc == nil || rc.Commit == nil || rc.Commit.Verification == nil {
		return nil
	}
	v := rc.Commit.Verification.GetVerified()
	return &v
}

// fetchLandedViaPR returns true if any PR's merge or commit list includes
// the given SHA. Cached per SHA across the run.
func (c *Connector) fetchLandedViaPR(ctx context.Context, owner, name, sha string, cache map[string]*bool) *bool {
	if v, ok := cache[sha]; ok {
		return v
	}
	prs, _, err := c.rest.PullRequests.ListPullRequestsWithCommit(ctx, owner, name, sha, &gh.ListOptions{PerPage: 1})
	if err != nil {
		cache[sha] = nil
		return nil
	}
	v := len(prs) > 0
	cache[sha] = &v
	return &v
}

