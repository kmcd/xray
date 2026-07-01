package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleTOML = `window = "2025-01-01..2025-06-30"
capture_harness_content = false

[teams]
unassigned = ["kmcd/foo", "kmcd/bar", "kmcd/baz"]

[connectors.github]
token = "ghp_x"

[connectors.github_actions]

[connectors.circleci]
token = "cc"

[connectors.sentry]
token = "st"
organization = "my-org"
[connectors.sentry.projects]
"api-backend" = "kmcd/foo"

[connectors.bugsnag]
token = "bs"
[connectors.bugsnag.projects]
"foo-api" = "kmcd/foo"

[connectors.honeycomb]
token = "hc"
dataset = "production"
`

func writeTempTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "xray.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoadSample(t *testing.T) {
	p := writeTempTOML(t, sampleTOML)
	cfg, meta, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if meta == nil {
		t.Fatal("meta is nil")
	}
	if cfg.Window.Raw != "2025-01-01..2025-06-30" {
		t.Errorf("window raw: %q", cfg.Window.Raw)
	}
	if got := cfg.Window.Start.Format("2006-01-02"); got != "2025-01-01" {
		t.Errorf("start: %s", got)
	}
	if got := cfg.Window.End.Format("2006-01-02"); got != "2025-06-30" {
		t.Errorf("end: %s", got)
	}
	if cfg.CaptureHarnessContent {
		t.Error("capture_harness_content should be false")
	}
	repos := cfg.Teams["unassigned"]
	if len(repos) != 3 {
		t.Fatalf("unassigned repos: %v", repos)
	}
	if cfg.Connectors.GitHub == nil || cfg.Connectors.GitHub.Token != "ghp_x" {
		t.Error("github token not loaded")
	}
	if cfg.Connectors.GitHubActions == nil || cfg.Connectors.GitHubActions.Token != "ghp_x" {
		t.Errorf("github_actions should inherit token, got %+v", cfg.Connectors.GitHubActions)
	}
	if cfg.Connectors.Sentry == nil || cfg.Connectors.Sentry.Organization != "my-org" {
		t.Error("sentry organization not loaded")
	}
	if got := cfg.Connectors.Sentry.Projects["api-backend"]; got != "kmcd/foo" {
		t.Errorf("sentry project map: %v", cfg.Connectors.Sentry.Projects)
	}
	if cfg.Connectors.Bugsnag == nil || cfg.Connectors.Bugsnag.Projects["foo-api"] != "kmcd/foo" {
		t.Error("bugsnag projects not loaded")
	}
	if cfg.Connectors.Honeycomb == nil || cfg.Connectors.Honeycomb.Dataset != "production" {
		t.Error("honeycomb dataset not loaded")
	}
}

func TestLoadBadWindow(t *testing.T) {
	p := writeTempTOML(t, `window = "not-a-window"
[teams]
team = ["a/b"]
`)
	if _, _, err := Load(p); err == nil {
		t.Fatal("expected error on malformed window")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, _, err := Load("/no/such/file.toml"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadPRWindow(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "ghp_x"
pr_window = "2024-06-15..2026-06-15"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Connectors.GitHub == nil {
		t.Fatal("github connector is nil")
	}
	if cfg.Connectors.GitHub.PRWindow == nil {
		t.Fatal("PRWindow is nil; expected non-nil")
	}
	if got := cfg.Connectors.GitHub.PRWindow.Start.Format("2006-01-02"); got != "2024-06-15" {
		t.Errorf("PRWindow.Start = %s, want 2024-06-15", got)
	}
	if got := cfg.Connectors.GitHub.PRWindow.End.Format("2006-01-02"); got != "2026-06-15" {
		t.Errorf("PRWindow.End = %s, want 2026-06-15", got)
	}
}

func TestLoadPRWindowOmitted(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "ghp_x"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Connectors.GitHub.PRWindow != nil {
		t.Errorf("PRWindow should be nil when omitted, got %+v", cfg.Connectors.GitHub.PRWindow)
	}
}

func TestLoadIssueLabels(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "ghp_x"
issue_bug_labels = ["bug", "type:bug"]
issue_regression_labels = ["regression", "regressed"]
[connectors.github.issue_severity_labels]
sev1 = "critical"
sev2 = "high"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gh := cfg.Connectors.GitHub
	if gh == nil {
		t.Fatal("github connector is nil")
	}
	if len(gh.IssueBugLabels) != 2 || gh.IssueBugLabels[0] != "bug" {
		t.Errorf("IssueBugLabels = %+v", gh.IssueBugLabels)
	}
	if len(gh.IssueRegressionLabels) != 2 || gh.IssueRegressionLabels[1] != "regressed" {
		t.Errorf("IssueRegressionLabels = %+v", gh.IssueRegressionLabels)
	}
	if gh.IssueSeverityLabels["sev1"] != "critical" || gh.IssueSeverityLabels["sev2"] != "high" {
		t.Errorf("IssueSeverityLabels = %+v", gh.IssueSeverityLabels)
	}
}

func TestLoadIssueLabelsOmitted(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "ghp_x"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gh := cfg.Connectors.GitHub
	// Defaults are applied connector-side (github.New), not in the parser:
	// the parsed config echoes empty fields verbatim.
	if gh.IssueBugLabels != nil || gh.IssueRegressionLabels != nil || gh.IssueSeverityLabels != nil {
		t.Errorf("expected nil issue-label fields when omitted, got bug=%+v reg=%+v sev=%+v",
			gh.IssueBugLabels, gh.IssueRegressionLabels, gh.IssueSeverityLabels)
	}
}

func TestLoadSparseHistoricalConfig(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "ghp_x"
pr_inflection = "2023-06-01"
pr_bracket_window = "12m"
pr_history_sample = "monthly:20"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gh := cfg.Connectors.GitHub
	if gh.PRInflection == nil {
		t.Fatal("PRInflection is nil")
	}
	if got := gh.PRInflection.Format("2006-01-02"); got != "2023-06-01" {
		t.Errorf("PRInflection = %s, want 2023-06-01", got)
	}
	if gh.PRBracketWindow == nil {
		t.Fatal("PRBracketWindow is nil")
	}
	if gh.PRBracketWindow.Months != 12 {
		t.Errorf("PRBracketWindow.Months = %d, want 12", gh.PRBracketWindow.Months)
	}
	if gh.PRBracketWindow.Raw != "12m" {
		t.Errorf("PRBracketWindow.Raw = %q, want 12m", gh.PRBracketWindow.Raw)
	}
	if gh.PRHistorySample == nil {
		t.Fatal("PRHistorySample is nil")
	}
	if gh.PRHistorySample.N != 20 {
		t.Errorf("PRHistorySample.N = %d, want 20", gh.PRHistorySample.N)
	}
	if gh.PRHistorySample.Random {
		t.Error("PRHistorySample.Random should be false")
	}
}

func TestLoadSparseHistoricalRandom(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "ghp_x"
pr_inflection = "2023-06-01"
pr_bracket_window = "12m"
pr_history_sample = "monthly:20:random"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Connectors.GitHub.PRHistorySample
	if s == nil {
		t.Fatal("PRHistorySample is nil")
	}
	if !s.Random {
		t.Error("PRHistorySample.Random should be true")
	}
	if s.Raw != "monthly:20:random" {
		t.Errorf("PRHistorySample.Raw = %q, want monthly:20:random", s.Raw)
	}
}

func TestLoadCircleCISparseConfig(t *testing.T) {
	p := writeTempTOML(t, `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.circleci]
token = "cc_x"
build_inflection = "2023-06-01"
build_bracket_window = "12m"
build_history_sample = "monthly:50"
[connectors.circleci.projects]
"gh/a/b" = "a/b"
`)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cc := cfg.Connectors.CircleCI
	if cc == nil {
		t.Fatal("CircleCI connector is nil")
	}
	if cc.BuildInflection == nil {
		t.Fatal("BuildInflection is nil")
	}
	if got := cc.BuildInflection.Format("2006-01-02"); got != "2023-06-01" {
		t.Errorf("BuildInflection = %s, want 2023-06-01", got)
	}
	if cc.BuildBracketWindow == nil {
		t.Fatal("BuildBracketWindow is nil")
	}
	if cc.BuildBracketWindow.Months != 12 {
		t.Errorf("BuildBracketWindow.Months = %d, want 12", cc.BuildBracketWindow.Months)
	}
	if cc.BuildBracketWindow.Raw != "12m" {
		t.Errorf("BuildBracketWindow.Raw = %q, want 12m", cc.BuildBracketWindow.Raw)
	}
	if cc.BuildHistorySample == nil {
		t.Fatal("BuildHistorySample is nil")
	}
	if cc.BuildHistorySample.N != 50 {
		t.Errorf("BuildHistorySample.N = %d, want 50", cc.BuildHistorySample.N)
	}
	if cc.BuildHistorySample.Random {
		t.Error("BuildHistorySample.Random should be false")
	}
}

func TestParseDurationSpec(t *testing.T) {
	tests := []struct {
		in      string
		years   int
		months  int
		days    int
		wantErr bool
	}{
		{"12m", 0, 12, 0, false},
		{"1y", 1, 0, 0, false},
		{"30d", 0, 0, 30, false},
		{"4w", 0, 0, 28, false},
		{"6m", 0, 6, 0, false},
		// invalid
		{"12", 0, 0, 0, true},
		{"12mo", 0, 0, 0, true},
		{"-3m", 0, 0, 0, true},
		{"0d", 0, 0, 0, true},
		{"", 0, 0, 0, true},
		{"m", 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseDurationSpec(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseDurationSpec(%q): expected error, got %+v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDurationSpec(%q): %v", tt.in, err)
			}
			if got.Years != tt.years || got.Months != tt.months || got.Days != tt.days {
				t.Errorf("parseDurationSpec(%q) = {Y:%d M:%d D:%d}, want {Y:%d M:%d D:%d}",
					tt.in, got.Years, got.Months, got.Days, tt.years, tt.months, tt.days)
			}
		})
	}
}

func TestParseHistorySample(t *testing.T) {
	tests := []struct {
		in       string
		n        int
		random   bool
		wantErr  bool
	}{
		{"monthly:20", 20, false, false},
		{"monthly:1", 1, false, false},
		{"monthly:100", 100, false, false},
		{"monthly:20:random", 20, true, false},
		// invalid
		{"monthly:0", 0, false, true},
		{"monthly:101", 0, false, true},
		{"weekly:20", 0, false, true},
		{"monthly", 0, false, true},
		{"20", 0, false, true},
		{"monthly:20:typo", 0, false, true},
		{"monthly:-1", 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseHistorySample(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseHistorySample(%q): expected error, got %+v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHistorySample(%q): %v", tt.in, err)
			}
			if got.N != tt.n || got.Random != tt.random {
				t.Errorf("parseHistorySample(%q) = {N:%d Random:%v}, want {N:%d Random:%v}",
					tt.in, got.N, got.Random, tt.n, tt.random)
			}
		})
	}
}

func TestLoadPullRequestOrder(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"created_asc", "created_asc", "created_asc", false},
		{"updated_desc", "updated_desc", "updated_desc", false},
		{"omitted defaults to empty", "", "", false},
		{"unknown value", "newest_first", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toml := "window = \"2025-01-01..2025-12-31\"\n[teams]\nt = [\"a/b\"]\n[connectors.github]\ntoken = \"x\"\n"
			if tt.value != "" {
				toml += "pull_request_order = \"" + tt.value + "\"\n"
			}
			p := writeTempTOML(t, toml)
			cfg, _, err := Load(p)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Load: expected error for pull_request_order=%q, got nil", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Connectors.GitHub.PROrder; got != tt.want {
				t.Errorf("PROrder = %q, want %q", got, tt.want)
			}
		})
	}
}
