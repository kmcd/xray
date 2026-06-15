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
