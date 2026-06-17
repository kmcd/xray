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

	// SecondsPerGBFileWalk covers the local filesystem walk performed by
	// extractWorkingTree after cloning. The walk itself makes zero GitHub
	// API calls but is significant wall-clock for large repos. Calibrated
	// from a 13-repo 7-day run: ~780s of walk time for ~1 GiB total disk.
	// 600 s/GiB is slightly below the empirical ~780 to leave headroom
	// while still capturing the dominant cost that the old formula missed.
	SecondsPerGBFileWalk = 600

	// SecondsPerGBComplexityHistory covers the extractComplexityHistoryBatch
	// phase: git cat-file decompression of (commit × file) blob pairs.
	// Calibrated from the issue-#149 profile: 11.4s on 478 MiB clone (75k
	// pairs) → ~3s after 4× parallelisation ≈ 6 s/GiB. Scaled ×10 for
	// production hardware (SecondsPerGBFileWalk calibration implies 5-10×
	// slower per-GiB on non-NVMe). Set above empirical to preserve the
	// cost-preview's over-estimate posture.
	SecondsPerGBComplexityHistory = 60

	// MinimumWallClockSeconds is the floor — a "trivial" plan (one tiny
	// repo, one connector) still has fixed overhead.
	MinimumWallClockSeconds = 30
)

// Plan is the cost-preview output for a configured run.
type Plan struct {
	Repos              int
	Teams              int
	WindowStart        time.Time
	WindowEnd          time.Time
	WindowDays         int
	Connectors         []string
	CloneBytes         int64
	APICalls           int
	WallClockSecs      int
	SuggestSparseMode  bool // true when window >2y, github active, no sparse-mode fields set
}

// RepoStat is a per-repo cheap-aggregate snapshot used to feed the cost
// estimate. The connector-specific probe implementation populates these
// fields via read-only GraphQL aggregates.
type RepoStat struct {
	Slug         string
	DiskUsageKB  int64 // GraphQL repository.diskUsage; 0 if unavailable.
	PullRequests int   // estimated PRs updated in window (all-time count scaled by window/repo-age ratio; unscaled if window not set).
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

	// SuggestSparseMode fires when the operator is on the expensive full-
	// enumeration path and hasn't configured any PR-narrowing option. The
	// 730-day threshold matches the 2-year heuristic used in the issue.
	// pull_request_order = "created_asc" handles long historical windows
	// efficiently on its own, so suppress the suggestion when it is set.
	if gh := cfg.Connectors.GitHub; gh != nil && p.WindowDays > 730 &&
		gh.PRInflection == nil && gh.PRWindow == nil && gh.PROrder != "created_asc" {
		p.SuggestSparseMode = true
	}

	// prScale adjusts the PR-cluster API-call estimate when the extraction
	// window is narrowed. Uniform-distribution assumption: PRs per day is
	// constant across the window. Sparse-historical mode (pr_inflection)
	// has two regions: the full-fidelity bracket+recent slice (scaled by
	// bracketDays/totalDays) and the sparse pre-bracket slice (scaled by
	// ~3 API calls per month bucket, which is much cheaper than a full walk).
	prScale := 1.0
	var sparseBucketCalls int
	if gh := cfg.Connectors.GitHub; gh != nil && p.WindowDays > 0 {
		switch {
		case gh.PRWindow != nil:
			prStart := gh.PRWindow.Start
			if prStart.Before(cfg.Window.Start) {
				prStart = cfg.Window.Start
			}
			prDays := windowDays(prStart, gh.PRWindow.End)
			if prDays < p.WindowDays {
				prScale = float64(prDays) / float64(p.WindowDays)
			}
		case gh.PRInflection != nil && gh.PRBracketWindow != nil:
			bw := gh.PRBracketWindow
			bracketStart := gh.PRInflection.AddDate(-bw.Years, -bw.Months, -bw.Days)
			if bracketStart.Before(cfg.Window.Start) {
				bracketStart = cfg.Window.Start
			}
			bracketDays := windowDays(bracketStart, cfg.Window.End)
			if bracketDays < p.WindowDays {
				prScale = float64(bracketDays) / float64(p.WindowDays)
			}
			if gh.PRHistorySample != nil && bracketStart.After(cfg.Window.Start) {
				// When bracketStart was clamped to window.Start, there is no
				// pre-bracket slice to fetch; guard avoids a spurious 3-call
				// estimate from windowDays returning 1 for a zero-width range.
				preBracketDays := windowDays(cfg.Window.Start, bracketStart)
				bucketMonths := (preBracketDays + 29) / 30 // ceil
				sparseBucketCalls = bucketMonths * 3
			}
		}
	}

	for _, s := range stats {
		p.CloneBytes += s.DiskUsageKB * CloneBytesPerKBDiskUsage
		p.APICalls += APICallsPerRepoBase
		p.APICalls += int(float64(s.PullRequests)*float64(APICallsPerPR)*prScale) + sparseBucketCalls
		p.APICalls += s.Commits * APICallsPerCommit
	}
	// Repos for which we have no stat still contribute the per-repo
	// fixed overhead so the API-call estimate isn't silently under-
	// reported when the probe fails partway through.
	if missing := p.Repos - len(stats); missing > 0 {
		p.APICalls += missing * APICallsPerRepoBase
	}

	diskGB := float64(p.CloneBytes) / float64(1<<30)
	api := float64(p.APICalls) * SecondsPerAPICall
	p.WallClockSecs = int(diskGB*SecondsPerGBClone + api + diskGB*SecondsPerGBFileWalk + diskGB*SecondsPerGBComplexityHistory)
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

