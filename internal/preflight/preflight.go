// Package preflight builds a cost-preview Plan for an xray run without
// performing the run. It is consumed by `xray check` to surface the
// expected wall-clock, disk, and API budget before the customer commits to
// an extraction.
//
// All probing is read-only: GraphQL count aggregates and metadata fields
// (diskUsage, pullRequests.totalCount) that mutate nothing.
package preflight

import (
	"fmt"
	"sort"
	"time"

	"github.com/kmcd/xray/internal/config"
)

// FormatBytes renders a byte count using 1024-based units with the
// matching binary unit labels (KiB / MiB / GiB) so the cost-preview's
// number and the post-run summary's artifact-size both compute and
// label the same way. Customers see one consistent format across both
// commands instead of "500 MB" in check and "476.8 MiB" in summary.
func FormatBytes(n int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// Calibration constants for the cost preview. These are deliberate
// over-estimates so the preview never under-promises wall-clock to a
// nervous customer. Refined empirically as the connectors stabilise.
const (
	// APICallsPerRepoBase is the cheap-overhead per repo: branch list,
	// branch_protection, languages, releases, codeowners, harness probe,
	// file_metrics summary. Roughly one paged call per endpoint.
	APICallsPerRepoBase = 40

	// APICallsPerPR is the average GraphQL+enrich load per PR: list page
	// share, reviews + comments + review_threads pagination, defects
	// enrich, merge-method fetch. Empirical median across the seed
	// engagements ~ 2.
	APICallsPerPR = 2

	// APICallsPerCommit is the per-commit enrich cost (batched, so this
	// is fractional in practice).
	APICallsPerCommit = 1

	// CloneBytesPerKBDiskUsage converts GitHub's diskUsage (KB) to a
	// clone-size estimate in bytes. diskUsage is the on-disk size of the
	// bare repository on GitHub's storage, which is typically a tight
	// upper bound for the bare clone xray performs (`git clone --bare`).
	CloneBytesPerKBDiskUsage = 1024

	// SecondsPerAPICall is the connector-side wall-clock budget per API
	// call, averaged across the GraphQL primary-rate ceiling, retry
	// jitter, and parallelism. Calibrated against the v0.1 baseline
	// (~3.5k calls / 7 min wall-clock on 5 workers).
	SecondsPerAPICall = 0.04

	// SecondsPerGBClone covers the clone phase: network bandwidth +
	// local disk write. Assumes a 200 Mbit/s effective downstream.
	SecondsPerGBClone = 40

	// MinimumWallClockSeconds is the floor — a "trivial" plan (one tiny
	// repo, one connector) still has fixed overhead.
	MinimumWallClockSeconds = 30
)

// Plan is the cost-preview output for a configured run.
type Plan struct {
	Repos          int
	Teams          int
	WindowStart    time.Time
	WindowEnd      time.Time
	WindowDays     int
	Connectors     []string
	CloneBytes     int64
	APICalls       int
	WallClockSecs  int
}

// RepoStat is a per-repo cheap-aggregate snapshot used to feed the cost
// estimate. The connector-specific probe implementation populates these
// fields via read-only GraphQL aggregates.
type RepoStat struct {
	Slug         string
	DiskUsageKB  int64 // GraphQL repository.diskUsage; 0 if unavailable.
	PullRequests int   // totalCount across all states.
	Commits      int   // estimated commits in window; 0 if unknown.
}

// BuildPlan composes a Plan from the config and the supplied per-repo
// stats. The function is pure: it does not consult the network. Callers
// fetch stats via a Prober (or pass nil stats for a config-only
// preview).
func BuildPlan(cfg *config.Config, stats []RepoStat) Plan {
	repos := cfg.AllRepos()
	sort.Strings(repos)
	p := Plan{
		Repos:       len(repos),
		Teams:       len(cfg.Teams),
		WindowStart: cfg.Window.Start,
		WindowEnd:   cfg.Window.End,
		WindowDays:  windowDays(cfg.Window.Start, cfg.Window.End),
		Connectors:  connectorNames(cfg),
	}

	for _, s := range stats {
		p.CloneBytes += s.DiskUsageKB * CloneBytesPerKBDiskUsage
		p.APICalls += APICallsPerRepoBase
		p.APICalls += s.PullRequests * APICallsPerPR
		p.APICalls += s.Commits * APICallsPerCommit
	}
	// Repos for which we have no stat still contribute the per-repo
	// fixed overhead so the API-call estimate isn't silently under-
	// reported when the probe fails partway through.
	if missing := p.Repos - len(stats); missing > 0 {
		p.APICalls += missing * APICallsPerRepoBase
	}

	clones := float64(p.CloneBytes) / float64(1<<30)
	api := float64(p.APICalls) * SecondsPerAPICall
	p.WallClockSecs = int(clones*SecondsPerGBClone + api)
	if p.WallClockSecs < MinimumWallClockSeconds {
		p.WallClockSecs = MinimumWallClockSeconds
	}
	return p
}

func windowDays(start, end time.Time) int {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	d := end.Sub(start)
	// inclusive day count: 2025-01-01..2025-06-30 → 181 days.
	return int(d.Hours()/24) + 1
}

// connectorNames returns the configured connector names in the
// canonical declaration order used elsewhere in the codebase.
func connectorNames(cfg *config.Config) []string {
	var out []string
	if cfg.Connectors.GitHub != nil {
		out = append(out, "github")
	}
	if cfg.Connectors.GitHubActions != nil {
		out = append(out, "github_actions")
	}
	if cfg.Connectors.CircleCI != nil {
		out = append(out, "circleci")
	}
	if cfg.Connectors.Sentry != nil {
		out = append(out, "sentry")
	}
	if cfg.Connectors.Bugsnag != nil {
		out = append(out, "bugsnag")
	}
	if cfg.Connectors.Honeycomb != nil {
		out = append(out, "honeycomb")
	}
	return out
}

// InaccessibleEndpoint records a permission-gated endpoint discovered to
// be inaccessible during preflight. Surfaced upfront so the customer can
// fix scope before starting the full run.
type InaccessibleEndpoint struct {
	Repo     string
	Endpoint string
	Reason   string
}

