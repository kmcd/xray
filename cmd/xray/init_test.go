package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/config"
)

// withFakeGitHub stands up an httptest.NewServer answering the orgs/<org>/repos
// listing with a fixed payload and swaps newGitHubClient to point at it for
// the duration of the test.
func withFakeGitHub(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/", func(w http.ResponseWriter, r *http.Request) {
		// Expect a path like /orgs/kmcd/repos.
		if !strings.HasSuffix(r.URL.Path, "/repos") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// No Link header → single-page response, go-github will not paginate.
		_, _ = w.Write([]byte(payload))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	orig := newGitHubClient
	newGitHubClient = func(ctx context.Context, token string) *github.Client {
		_ = ctx
		_ = token
		c := github.NewClient(srv.Client())
		base, _ := url.Parse(srv.URL + "/")
		c.BaseURL = base
		return c
	}
	t.Cleanup(func() { newGitHubClient = orig })

	return srv
}

func TestInitCmd_RoundTripsThroughValidate(t *testing.T) {
	payload := `[
		{"name": "foo", "full_name": "kmcd/foo"},
		{"name": "bar", "full_name": "kmcd/bar"}
	]`
	withFakeGitHub(t, payload)

	outPath := filepath.Join(t.TempDir(), "xray.toml")

	root, stdout, stderr := newTestRoot(t)
	root.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("init err: %v (stderr=%q)", err, stderr.String())
	}

	// Scaffold file was written.
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read scaffold: %v", err)
	}
	bs := string(body)

	// Repos are sorted, both must be present in the unassigned team.
	if !strings.Contains(bs, `"kmcd/bar"`) || !strings.Contains(bs, `"kmcd/foo"`) {
		t.Errorf("scaffold missing expected repos: %q", bs)
	}
	if i, j := strings.Index(bs, `"kmcd/bar"`), strings.Index(bs, `"kmcd/foo"`); i > j {
		t.Errorf("expected kmcd/bar to precede kmcd/foo in sorted scaffold output")
	}
	if !strings.Contains(stdout.String(), "wrote ") {
		t.Errorf("init stdout = %q, want 'wrote' line", stdout.String())
	}

	// Round-trip: feed the scaffold to config.Load + config.Validate
	// directly (skipping the cobra layer so we are not double-testing it).
	// The scaffold is intentionally a starting point — it has an empty
	// window and empty connector tokens. We assert the *exact* set of
	// diagnostics it yields, so any future drift between scaffold and
	// validator surfaces here.
	cfg, meta, loadErr := config.Load(outPath)
	if loadErr != nil {
		t.Fatalf("scaffold failed config.Load: %v", loadErr)
	}
	diags := config.Validate(cfg, meta, outPath)
	gotPaths := make([]string, 0, len(diags))
	for _, d := range diags {
		gotPaths = append(gotPaths, d.Path+": "+d.Msg)
	}
	sort.Strings(gotPaths)

	wantPaths := []string{
		`connectors.bugsnag: missing required key "projects"`,
		`connectors.bugsnag: missing required key "token"`,
		`connectors.circleci: missing required key "projects"`,
		`connectors.circleci: missing required key "token"`,
		`connectors.github: missing required key "token"`,
		`connectors.github_actions: missing token (and no token to inherit from [connectors.github])`,
		`connectors.honeycomb: missing required key "dataset"`,
		`connectors.honeycomb: missing required key "token"`,
		`connectors.sentry: missing required key "organization"`,
		`connectors.sentry: missing required key "projects"`,
		`connectors.sentry: missing required key "token"`,
		`window: missing required key "window"`,
	}
	sort.Strings(wantPaths)

	if !equalStringSlices(gotPaths, wantPaths) {
		t.Errorf("scaffold diagnostics mismatch:\ngot:\n  %s\nwant:\n  %s",
			strings.Join(gotPaths, "\n  "),
			strings.Join(wantPaths, "\n  "),
		)
	}
}

func TestInitCmd_FilledScaffoldValidatesCleanly(t *testing.T) {
	// Take the scaffold the round-trip test verifies, fill in the minimal
	// required fields, and confirm the result is diagnostic-free. This is
	// the "happy path" half of the scaffold-validate contract.
	payload := `[{"name": "foo", "full_name": "kmcd/foo"}]`
	withFakeGitHub(t, payload)

	outPath := filepath.Join(t.TempDir(), "xray.toml")
	root, _, _ := newTestRoot(t)
	root.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("init err: %v", err)
	}

	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read scaffold: %v", err)
	}
	// Fill in window, drop every connector block by overwriting with a
	// minimal valid config that keeps the discovered teams. The teams block
	// is what we are asserting init produces correctly.
	filled := strings.Replace(string(body), `window = ""`,
		`window = "2025-01-01..2025-06-30"`, 1)
	// Strip every [connectors.*] block. Easier than filling each in.
	if i := strings.Index(filled, "[connectors."); i >= 0 {
		filled = filled[:i]
	}
	if err := os.WriteFile(outPath, []byte(filled), 0o600); err != nil {
		t.Fatalf("write filled: %v", err)
	}

	cfg, meta, err := config.Load(outPath)
	if err != nil {
		t.Fatalf("load filled: %v", err)
	}
	if diags := config.Validate(cfg, meta, outPath); len(diags) != 0 {
		got := make([]string, 0, len(diags))
		for _, d := range diags {
			got = append(got, d.Error())
		}
		t.Errorf("filled scaffold has diagnostics:\n  %s", strings.Join(got, "\n  "))
	}
}

func TestInitCmd_RefuseOverwrite(t *testing.T) {
	payload := `[{"name": "foo", "full_name": "kmcd/foo"}]`
	withFakeGitHub(t, payload)

	outPath := filepath.Join(t.TempDir(), "xray.toml")
	if err := os.WriteFile(outPath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// First run without --force: must refuse and leave the file untouched.
	root, _, _ := newTestRoot(t)
	root.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath})
	err := root.Execute()
	if err == nil {
		t.Fatal("init err = nil, want error refusing to overwrite")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want 'already exists'", err)
	}
	body, _ := os.ReadFile(outPath)
	if string(body) != "existing" {
		t.Errorf("file modified without --force: %q", string(body))
	}

	// Second run with --force: file is overwritten with the scaffold.
	root2, _, _ := newTestRoot(t)
	root2.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath, "--force"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("init --force err: %v", err)
	}
	body, _ = os.ReadFile(outPath)
	if string(body) == "existing" {
		t.Error("file not overwritten with --force")
	}
	if !strings.Contains(string(body), `"kmcd/foo"`) {
		t.Errorf("overwritten file missing scaffold content: %q", string(body))
	}
}

func TestInitCmd_QuietSuppressesSuccessLine(t *testing.T) {
	payload := `[{"name": "foo", "full_name": "kmcd/foo"}]`
	withFakeGitHub(t, payload)
	outPath := filepath.Join(t.TempDir(), "xray.toml")
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath, "--output", "quiet"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init err: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("quiet stdout = %q, want empty", stdout.String())
	}
}

func TestInitCmd_JSONEmitsSummary(t *testing.T) {
	payload := `[{"name": "foo", "full_name": "kmcd/foo"}]`
	withFakeGitHub(t, payload)
	outPath := filepath.Join(t.TempDir(), "xray.toml")
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath, "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init err: %v", err)
	}
	line := strings.TrimSpace(stdout.String())
	if !strings.Contains(line, `"kind":"init_summary"`) {
		t.Errorf("stdout = %q, want init_summary line", line)
	}
	if !strings.Contains(line, `"ok":true`) {
		t.Errorf("stdout = %q, want ok=true", line)
	}
	if !strings.Contains(line, `"overwritten":false`) {
		t.Errorf("stdout = %q, want overwritten=false", line)
	}
}

func TestInitCmd_JSONOverwrittenTrue(t *testing.T) {
	payload := `[{"name": "foo", "full_name": "kmcd/foo"}]`
	withFakeGitHub(t, payload)
	outPath := filepath.Join(t.TempDir(), "xray.toml")
	if err := os.WriteFile(outPath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"init", "--org", "kmcd", "--token", "x", "--out", outPath, "--force", "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("init err: %v", err)
	}
	if !strings.Contains(stdout.String(), `"overwritten":true`) {
		t.Errorf("stdout = %q, want overwritten=true", stdout.String())
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
