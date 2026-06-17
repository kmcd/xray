package github

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

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

	// Snapshot connector-wide counters before extraction begins. Deltas at
	// exit approximate per-repo usage; concurrent Extract calls mean the
	// totals across all provenances are accurate but per-repo values are
	// approximations.
	c.gqlMu.Lock()
	extractStartUsed := c.gqlPointsUsed
	c.gqlMu.Unlock()
	extractStartCancelRetries := c.streamCancelRetries.Load()

	// Record pr_window or sparse-historical sampling config in provenance.
	// Must run before the parallel block so the main provenance carries the
	// value regardless of which fragment the API-bound goroutine writes to.
	if c.prWindow != nil {
		eff := c.effectivePRWindow(window)
		if prov.ConfigDepth == nil {
			prov.ConfigDepth = make(map[string]string)
		}
		prov.ConfigDepth["pr_window"] = eff.Start.Format("2006-01-02") + ".." + eff.End.Format("2006-01-02")
	} else if c.bracketStart != nil {
		if prov.ConfigDepth == nil {
			prov.ConfigDepth = make(map[string]string)
		}
		eff := c.effectivePRWindow(window)
		prov.ConfigDepth["pr_window"] = eff.Start.Format("2006-01-02") + ".." + eff.End.Format("2006-01-02")
		if c.sampleSpec != nil {
			prov.ConfigDepth["pr_history_sample"] = c.sampleSpec.Raw
		}
		// SamplingProvenance (with per-bucket Buckets slice) is initialized on
		// provB inside goroutine B, not here. Initializing it on prov here would
		// mean extractSparsePRs (which receives &provB) appends to the wrong
		// Sampling pointer — provB.Sampling would stay nil, so the guard inside
		// extractSparsePRs would never fire and all bucket records would be lost.
		// Merge (first-wins) copies provB.Sampling to prov after the parallel
		// block completes.
	}

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

	c.insertTeamMapping(repo, sink, &prov)

	// --- Phase 2: parallel block ------------------------------------------

	provA := connector.NewProvenance(c.Name(), repo.Slug, window)
	provB := connector.NewProvenance(c.Name(), repo.Slug, window)
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: clone-bound stages.
	go func() {
		defer wg.Done()
		c.extractBranches(ctx, repo, sink, &provA)
		c.extractCodeowners(ctx, repo, sink, &provA)
		c.extractReleases(ctx, repo, window, sink, &provA)
		c.extractCommits(ctx, repo, window, sink, &provA, mm)
		// Single walk for languages + file_metrics + harness (replaces three
		// separate filepath.Walk passes).
		c.extractWorkingTree(ctx, repo, window, sink, &provA)
		c.extractRepoFiles(ctx, repo, sink, &provA)
	}()

	// Goroutine B: API-bound PR stage. Uses prefetch cache when present.
	// The effective PR window covers the bracket+recent slice (pr_window or
	// bracketStart when sparse mode is active, else the full global window).
	// The sparse pre-bracket slice runs sequentially after the bracket walk
	// so the two emitters do not race on emitPR batch handles (#167).
	go func() {
		defer wg.Done()
		bracketAndRecent := c.effectivePRWindow(window)
		c.extractPRs(ctx, repo, bracketAndRecent, sink, &provB)

		if c.bracketStart != nil {
			strategy := "search_default_relevance"
			if c.sampleSpec != nil && c.sampleSpec.Random {
				strategy = "random"
			}
			// Initialize provB.Sampling here (not in the sync prelude on prov)
			// so that extractSparsePRs can append bucket records to the same
			// Sampling pointer it receives via &provB. Merge (Phase 3) copies
			// provB.Sampling to prov under first-wins policy since prov.Sampling
			// is nil at merge time.
			provB.Sampling = &connector.SamplingProvenance{
				InflectionDate: c.inflection.Format("2006-01-02"),
				BracketWindow:  c.bracketSpec.Raw,
				BracketStart:   c.bracketStart.Format("2006-01-02"),
				BracketEnd:     window.End.Format("2006-01-02"),
				Strategy:       strategy,
			}

			if c.sampleSpec != nil {
				sparseSlice := connector.Window{
					Start: window.Start,
					End:   bracketAndRecent.Start.Add(-time.Second),
				}
				if sparseSlice.End.After(sparseSlice.Start) {
					c.extractSparsePRs(ctx, repo, sparseSlice, c.sampleSpec, sink, &provB)
				}
			}
		}
	}()

	wg.Wait()

	// --- Phase 3: sync postlude -------------------------------------------

	prov.Merge(provA)
	prov.Merge(provB)

	if err := ctx.Err(); err != nil {
		prov.PaginationComplete = false
	}

	// Copy connector-wide counters into provenance as per-extraction deltas.
	c.gqlMu.Lock()
	prov.GraphQLPointsUsed = c.gqlPointsUsed - extractStartUsed
	prov.GraphQLPointsRemaining = c.gqlPointsRemaining
	c.gqlMu.Unlock()
	prov.StreamCancelRetries = int(c.streamCancelRetries.Load() - extractStartCancelRetries)

	return prov
}

// insertRepoRow fetches repo metadata via REST and emits the repos row.
// HeadSHA, default branch, and team come from the connector.Repo struct
// (already resolved by the caller from the local clone); the rest is
// pulled from the API.
func (c *Connector) insertRepoRow(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) error {
	row := model.Repo{
		Slug:          repo.Slug,
		DefaultBranch: repo.DefaultBranch,
		HeadSHA:       repo.HeadSHA,
		Team:          repo.Team,
	}
	if owner, name, ok := splitSlug(repo.Slug); ok {
		c.enrichRepoMetadata(ctx, repo.Slug, owner, name, &row, prov)
		c.enrichContributorCount(ctx, owner, name, &row, prov)
	} else {
		reason := "invalid slug: " + repo.Slug
		prov.Endpoints["repo_metadata"] = connector.EndpointStatus{Accessible: false, Reason: reason}
		prov.Endpoints["contributors"] = connector.EndpointStatus{Accessible: false, Reason: reason}
	}
	if err := sink.InsertRepo(row); err != nil {
		return err
	}
	prov.RowsReturned["repos"]++
	return nil
}

// enrichRepoMetadata fills repo-row enrichment fields from the REST repo-Get
// endpoint and records EndpointStatus["repo_metadata"] on every code path.
func (c *Connector) enrichRepoMetadata(ctx context.Context, slug, owner, name string, row *model.Repo, prov *connector.Provenance) {
	r, _, err := c.rest.Repositories.Get(ctx, owner, name)
	if err != nil {
		prov.Endpoints["repo_metadata"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
		c.log.Warn("github: get repo metadata",
			slog.String("repo", slug),
			slog.String("error", err.Error()),
		)
		return
	}
	prov.Endpoints["repo_metadata"] = connector.EndpointStatus{Accessible: true}
	if r == nil {
		return
	}
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
}

// insertTeamMapping emits the team -> repo mapping row when the repo carries a
// team. Records the row in provenance on success and the error on failure.
func (c *Connector) insertTeamMapping(repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
	if repo.Team == "" {
		return
	}
	if err := sink.InsertTeamRepo(repo.Team, repo.Slug); err != nil {
		prov.Errors["teams"] = err.Error()
		return
	}
	prov.RowsReturned["teams"]++
}

// enrichContributorCount populates ContributorCount via the contributors list
// endpoint and records EndpointStatus["contributors"] on every code path.
func (c *Connector) enrichContributorCount(ctx context.Context, owner, name string, row *model.Repo, prov *connector.Provenance) {
	n, err := c.countContributors(ctx, owner, name)
	if err != nil {
		prov.Endpoints["contributors"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
		if prov.Errors["contributors"] == "" {
			prov.Errors["contributors"] = err.Error()
		}
		return
	}
	prov.Endpoints["contributors"] = connector.EndpointStatus{Accessible: true}
	row.ContributorCount = n
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
