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
