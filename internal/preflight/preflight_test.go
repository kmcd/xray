package preflight

import (
	"reflect"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/config"
)

func TestBuildPlan_WindowAndCounts(t *testing.T) {
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
		},
		Teams: map[string][]string{
			"core": {"kmcd/foo", "kmcd/bar"},
			"data": {"kmcd/baz"},
		},
		Connectors: config.Connectors{
			GitHub:        &config.GitHubConn{Token: "x"},
			GitHubActions: &config.GitHubActionsConn{Token: "x"},
		},
	}
	stats := []RepoStat{
		{Slug: "kmcd/foo", DiskUsageKB: 500_000, PullRequests: 200, Commits: 1000},
		{Slug: "kmcd/bar", DiskUsageKB: 100_000, PullRequests: 50, Commits: 300},
		{Slug: "kmcd/baz", DiskUsageKB: 50_000, PullRequests: 25, Commits: 100},
	}

	p := BuildPlan(cfg, stats)

	if p.Repos != 3 {
		t.Errorf("Repos = %d, want 3", p.Repos)
	}
	if p.Teams != 2 {
		t.Errorf("Teams = %d, want 2", p.Teams)
	}
	if p.WindowDays != 181 {
		t.Errorf("WindowDays = %d, want 181", p.WindowDays)
	}
	wantConnectors := []string{"github", "github_actions"}
	if !reflect.DeepEqual(p.Connectors, wantConnectors) {
		t.Errorf("Connectors = %v, want %v", p.Connectors, wantConnectors)
	}
	wantClone := int64(650_000) * CloneBytesPerKBDiskUsage
	if p.CloneBytes != wantClone {
		t.Errorf("CloneBytes = %d, want %d", p.CloneBytes, wantClone)
	}
	wantAPI := 3*APICallsPerRepoBase + (200+50+25)*APICallsPerPR + (1000+300+100)*APICallsPerCommit
	if p.APICalls != wantAPI {
		t.Errorf("APICalls = %d, want %d", p.APICalls, wantAPI)
	}
	if p.WallClockSecs < MinimumWallClockSeconds {
		t.Errorf("WallClockSecs = %d, below floor", p.WallClockSecs)
	}
}

func TestBuildPlan_FloorEnforced(t *testing.T) {
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Teams:      map[string][]string{"t": {"k/r"}},
		Connectors: config.Connectors{GitHub: &config.GitHubConn{Token: "x"}},
	}
	p := BuildPlan(cfg, []RepoStat{{Slug: "k/r"}})
	if p.WallClockSecs != MinimumWallClockSeconds {
		t.Errorf("WallClockSecs = %d, want floor %d", p.WallClockSecs, MinimumWallClockSeconds)
	}
}

func TestBuildPlan_MissingStatsStillContributeBaseAPI(t *testing.T) {
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
		},
		Teams:      map[string][]string{"t": {"k/a", "k/b"}},
		Connectors: config.Connectors{GitHub: &config.GitHubConn{Token: "x"}},
	}
	// Only one stat returned (probe partially failed). The plan still
	// charges a per-repo base for the missing one.
	p := BuildPlan(cfg, []RepoStat{{Slug: "k/a", PullRequests: 10}})
	wantAPI := APICallsPerRepoBase + 10*APICallsPerPR + APICallsPerRepoBase
	if p.APICalls != wantAPI {
		t.Errorf("APICalls = %d, want %d", p.APICalls, wantAPI)
	}
}

func TestBuildPlan_PRWindowScaling(t *testing.T) {
	// Global window: 2021-01-01..2022-12-31 = 730 days
	// PR window:     2022-01-01..2022-12-31 = 365 days
	// prScale = 365/730 = 0.5 exactly
	prStart := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	prEnd := time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		Teams: map[string][]string{"t": {"a/b"}},
		Connectors: config.Connectors{
			GitHub: &config.GitHubConn{
				Token:    "x",
				PRWindow: &config.Window{Start: prStart, End: prEnd},
			},
		},
	}
	stats := []RepoStat{{Slug: "a/b", PullRequests: 100, Commits: 1000}}
	p := BuildPlan(cfg, stats)

	// prScale = 365/730 = 0.5; PR contribution is halved.
	wantAPI := APICallsPerRepoBase + int(float64(100)*float64(APICallsPerPR)*0.5) + 1000*APICallsPerCommit
	if p.APICalls != wantAPI {
		t.Errorf("APICalls = %d, want %d (with pr_window scaling)", p.APICalls, wantAPI)
	}

	// Without pr_window the estimate is larger.
	cfgFull := *cfg
	cfgFull.Connectors.GitHub = &config.GitHubConn{Token: "x"}
	if p.APICalls >= BuildPlan(&cfgFull, stats).APICalls {
		t.Errorf("pr_window scaling should reduce APICalls")
	}
}

func TestWindowDays(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
		want       int
	}{
		{"6-month inclusive",
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
			181,
		},
		{"single day",
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			1,
		},
		{"end-before-start", time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC), time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 0},
		{"zero",
			time.Time{}, time.Time{}, 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowDays(tc.start, tc.end); got != tc.want {
				t.Errorf("windowDays(%s, %s) = %d, want %d", tc.start, tc.end, got, tc.want)
			}
		})
	}
}

func TestConnectorNames_DeclarationOrder(t *testing.T) {
	cfg := &config.Config{
		Connectors: config.Connectors{
			Honeycomb:     &config.HoneycombConn{Token: "x", Dataset: "d"},
			Sentry:        &config.SentryConn{Token: "x", Organization: "o"},
			GitHub:        &config.GitHubConn{Token: "x"},
			GitHubActions: &config.GitHubActionsConn{Token: "x"},
		},
	}
	got := connectorNames(cfg)
	want := []string{"github", "github_actions", "sentry", "honeycomb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connectorNames = %v, want %v", got, want)
	}
}

func TestFormatBytes_Boundaries(t *testing.T) {
	const (
		kib int64 = 1024
		mib       = 1024 * kib
		gib       = 1024 * mib
	)
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"sub-kib", 512, "512 B"},
		{"kib-boundary-low", kib - 1, "1023 B"},
		{"kib-boundary-high", kib, "1.0 KiB"},
		{"mib-boundary-low", mib - 1, "1024.0 KiB"},
		{"mib-boundary-high", mib, "1.0 MiB"},
		{"gib-boundary-low", gib - 1, "1024.0 MiB"},
		{"gib-boundary-high", gib, "1.0 GiB"},
		{"large-gib", 2*gib + gib/2, "2.5 GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatBytes(tt.in); got != tt.want {
				t.Errorf("FormatBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
