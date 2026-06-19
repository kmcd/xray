package run_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/run"
)

// stubConnector is an in-test connector that records rows of a fixed shape so
// the integration test can assert on the post-run SQLite and manifest without
// depending on any real provider implementation.
type stubConnector struct {
	name     string
	commits  int
	prs      int
	deploys  []model.Deploy
	errKey   string
	errValue string
}

func (s *stubConnector) Name() string                       { return s.name }
func (s *stubConnector) Ping(ctx context.Context) error     { return nil }
func (s *stubConnector) Extract(ctx context.Context, repo connector.Repo, w connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(s.name, repo.Slug, w)

	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)

	for i := 0; i < s.commits; i++ {
		sha := fmt.Sprintf("%040x", i+1)
		if err := sink.InsertCommit(model.Commit{
			SHA:         sha,
			Repo:        repo.Slug,
			AuthoredAt:  base.Add(time.Duration(i) * time.Hour),
			CommittedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			prov.Errors[fmt.Sprintf("commit_%d", i)] = err.Error()
			return prov
		}
		prov.RowsReturned["commits"]++
	}

	for i := 0; i < s.prs; i++ {
		if err := sink.InsertPR(model.PR{
			Number:   i + 1,
			Repo:     repo.Slug,
			Title:    fmt.Sprintf("PR %d", i+1),
			OpenedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			prov.Errors[fmt.Sprintf("pr_%d", i)] = err.Error()
			return prov
		}
		prov.RowsReturned["prs"]++
	}

	for _, d := range s.deploys {
		d.Repo = repo.Slug
		if err := sink.InsertDeploy(d); err != nil {
			prov.Errors[fmt.Sprintf("deploy_%s", d.ID)] = err.Error()
			return prov
		}
		prov.RowsReturned["deploys"]++
	}

	if s.errKey != "" {
		prov.Errors[s.errKey] = s.errValue
		prov.PaginationComplete = false
	}

	return prov
}

// setupFakeRemote builds a bare git repo on disk and a working tree with a
// few commits including a rename and a merge, then pushes it into the bare.
// Returns the bare-repo path so the caller can install an insteadOf rewrite.
func setupFakeRemote(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	bare := filepath.Join(root, "bare.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	runGit(t, bare, "init", "--bare", "-q", "-b", "main")

	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	runGit(t, work, "init", "-q", "-b", "main")
	runGit(t, work, "config", "user.email", "fixture@example.com")
	runGit(t, work, "config", "user.name", "Fixture")
	runGit(t, work, "config", "commit.gpgsign", "false")
	runGit(t, work, "config", "tag.gpgsign", "false")

	writeFile(t, filepath.Join(work, "README.md"), "# fixture\n")
	writeFile(t, filepath.Join(work, "src/a.go"), "package fixture\n")
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-q", "-m", "initial")

	// Rename a file in its own commit.
	runGit(t, work, "mv", "src/a.go", "src/b.go")
	runGit(t, work, "commit", "-q", "-m", "rename a to b")

	// Side branch with a different file, then merge back to main.
	runGit(t, work, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(work, "src/c.go"), "package fixture\n")
	runGit(t, work, "add", "src/c.go")
	runGit(t, work, "commit", "-q", "-m", "feature: add c")
	runGit(t, work, "checkout", "-q", "main")
	runGit(t, work, "merge", "--no-ff", "-q", "-m", "merge feature", "feature")

	runGit(t, work, "remote", "add", "origin", bare)
	runGit(t, work, "push", "-q", "origin", "main")

	return bare
}

// installInsteadOf points all https://github.com/* URLs at file://<bare>/
// for the duration of the test by writing a GIT_CONFIG_GLOBAL file. The
// gitcli package hardcodes the GitHub URL; we redirect at the git level.
func installInsteadOf(t *testing.T, slugToBare map[string]string) {
	t.Helper()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config")

	var buf bytes.Buffer
	// Stable ordering so the file is deterministic.
	slugs := make([]string, 0, len(slugToBare))
	for s := range slugToBare {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		bare := slugToBare[slug]
		fmt.Fprintf(&buf, "[url \"file://%s\"]\n\tinsteadOf = https://github.com/%s.git\n", bare, slug)
	}
	if err := os.WriteFile(cfgPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write git config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfgPath)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	// Isolate from the user's git config so fixture commits are deterministic.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_AUTHOR_DATE=2025-03-01T12:00:00Z",
		"GIT_COMMITTER_DATE=2025-03-01T12:00:00Z",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func standardCfg(slug string) *config.Config {
	return &config.Config{
		Window: config.Window{
			// shallow-since back-dates by 30 days, so use a recent window
			// whose start - 30d still precedes the fixture's commit dates.
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
			Raw:   "2025-01-01..2025-06-30",
		},
		Teams: map[string][]string{"platform": {slug}},
	}
}

// extractArtifact extracts the tar.gz at path into a fresh temp dir and
// returns that dir plus the parsed manifest as a generic map.
func extractArtifact(t *testing.T, path string) (string, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	entries := readTarGz(t, path)
	for name, data := range entries {
		out := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
	}
	var m map[string]any
	mb, ok := entries["manifest.json"]
	if !ok {
		t.Fatalf("manifest.json missing from archive")
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatalf("manifest parse: %v", err)
	}
	return dir, m
}

func TestRun_EndToEnd_StubConnector(t *testing.T) {
	slug := "owner/fixture"
	bare := setupFakeRemote(t)
	installInsteadOf(t, map[string]string{slug: bare})

	stub := &stubConnector{name: "stub", commits: 3, prs: 2}

	cfg := standardCfg(slug)
	out := filepath.Join(t.TempDir(), "artifact.tar.gz")
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		Connectors:  []connector.Connector{stub},
	}

	result, err := run.Run(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(result.ArtifactPath); err != nil {
		t.Fatalf("artifact missing: %v", err)
	}

	dir, m := extractArtifact(t, result.ArtifactPath)

	// Schema row in the SQLite matches the package constant.
	db, err := sql.Open("sqlite", filepath.Join(dir, "metrics.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	var sv int
	if err := db.QueryRowContext(t.Context(), `SELECT schema_version FROM _schema`).Scan(&sv); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if sv != model.SchemaVersion {
		t.Errorf("schema_version: got %d want %d", sv, model.SchemaVersion)
	}

	// Row counts in the DB match what the stub inserted.
	var commitCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM commits WHERE repo = ?`, slug).Scan(&commitCount); err != nil {
		t.Fatalf("count commits: %v", err)
	}
	if commitCount != stub.commits {
		t.Errorf("commits: got %d want %d", commitCount, stub.commits)
	}
	var prCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM prs WHERE repo = ?`, slug).Scan(&prCount); err != nil {
		t.Fatalf("count prs: %v", err)
	}
	if prCount != stub.prs {
		t.Errorf("prs: got %d want %d", prCount, stub.prs)
	}

	// Manifest cross-checks.
	if got := int(m["schema_version"].(float64)); got != model.SchemaVersion {
		t.Errorf("manifest schema_version: got %d want %d", got, model.SchemaVersion)
	}
	cu, ok := m["connectors_used"].([]any)
	if !ok {
		t.Fatalf("connectors_used not a slice: %T", m["connectors_used"])
	}
	foundStub := false
	for _, c := range cu {
		if c == "stub" {
			foundStub = true
			break
		}
	}
	if !foundStub {
		t.Errorf("connectors_used missing stub: %v", cu)
	}

	counts, ok := m["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts not a map: %T", m["counts"])
	}
	if got := int(counts["commits"].(float64)); got != stub.commits {
		t.Errorf("counts.commits: got %d want %d", got, stub.commits)
	}
	if got := int(counts["prs"].(float64)); got != stub.prs {
		t.Errorf("counts.prs: got %d want %d", got, stub.prs)
	}

	provs, ok := m["extraction_provenance"].([]any)
	if !ok {
		t.Fatalf("extraction_provenance not a slice: %T", m["extraction_provenance"])
	}
	foundStubProv := false
	for _, p := range provs {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if pm["connector"] != "stub" {
			continue
		}
		foundStubProv = true
		rows, ok := pm["rows_returned"].(map[string]any)
		if !ok {
			t.Fatalf("rows_returned not a map: %T", pm["rows_returned"])
		}
		if got := int(rows["commits"].(float64)); got != stub.commits {
			t.Errorf("provenance commits: got %d want %d", got, stub.commits)
		}
		if got := int(rows["prs"].(float64)); got != stub.prs {
			t.Errorf("provenance prs: got %d want %d", got, stub.prs)
		}
		if pm["repo"] != slug {
			t.Errorf("provenance repo: got %v want %s", pm["repo"], slug)
		}
	}
	if !foundStubProv {
		t.Errorf("no stub provenance entry in manifest")
	}

	// Manifest records the repo with a populated head_sha (came from the
	// real clone of the fixture).
	repos, ok := m["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("repos shape: %T %v", m["repos"], m["repos"])
	}
	repo0 := repos[0].(map[string]any)
	if repo0["slug"] != slug {
		t.Errorf("repo slug: got %v want %s", repo0["slug"], slug)
	}
	if head, _ := repo0["head_sha"].(string); head == "" {
		t.Errorf("head_sha empty; clone path may not have populated it")
	}
}

func TestRun_PostprocessSurfaced(t *testing.T) {
	slug := "owner/fixture"
	bare := setupFakeRemote(t)
	installInsteadOf(t, map[string]string{slug: bare})

	// Rollback pattern per ADR 017: D[0]=A (success), D[1]=B (failed),
	// D[2]=A (success) in the same env triggers the rollback link because
	// the predecessor D[1] is non-success. deployed_at must be strictly
	// increasing.
	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	deploys := []model.Deploy{
		{ID: "d0", Environment: "prod", DeployedAt: base, CommitSHA: "shaA", Source: "stub", Status: "success"},
		{ID: "d1", Environment: "prod", DeployedAt: base.Add(time.Hour), CommitSHA: "shaB", Source: "stub", Status: "failed"},
		{ID: "d2", Environment: "prod", DeployedAt: base.Add(2 * time.Hour), CommitSHA: "shaA", Source: "stub", Status: "success"},
	}
	stub := &stubConnector{name: "stub", deploys: deploys}

	cfg := standardCfg(slug)
	out := filepath.Join(t.TempDir(), "artifact.tar.gz")
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		Connectors:  []connector.Connector{stub},
	}

	result, err := run.Run(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	dir, _ := extractArtifact(t, result.ArtifactPath)
	db, err := sql.Open("sqlite", filepath.Join(dir, "metrics.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	// D[1] should be marked rolled_back, D[2] should point at D[1].
	var rolled int
	if err := db.QueryRowContext(t.Context(), `SELECT rolled_back FROM deploys WHERE id = 'd1' AND repo = ?`, slug).Scan(&rolled); err != nil {
		t.Fatalf("query d1: %v", err)
	}
	var supersedes sql.NullString
	if err := db.QueryRowContext(t.Context(), `SELECT supersedes_deploy_id FROM deploys WHERE id = 'd2' AND repo = ?`, slug).Scan(&supersedes); err != nil {
		t.Fatalf("query d2: %v", err)
	}

	// Per ADR 017: D[1] failed, so the rollback heuristic triggers and
	// D[2] supersedes D[1].
	if rolled != 1 {
		t.Errorf("d1.rolled_back = %d, want 1", rolled)
	}
	if supersedes.String != "d1" {
		t.Errorf("d2.supersedes_deploy_id = %q, want d1", supersedes.String)
	}
}

func TestRun_FailedConnectorReported(t *testing.T) {
	slug := "owner/fixture"
	bare := setupFakeRemote(t)
	installInsteadOf(t, map[string]string{slug: bare})

	stub := &stubConnector{
		name:     "stub",
		commits:  1,
		errKey:   "something",
		errValue: "stub-failure",
	}

	cfg := standardCfg(slug)
	out := filepath.Join(t.TempDir(), "artifact.tar.gz")
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		Connectors:  []connector.Connector{stub},
	}

	result, err := run.Run(context.Background(), cfg, opts)
	if err == nil {
		t.Errorf("Run: expected non-nil error when a connector populates Errors")
	}
	// Even with a connector error, the artifact must still be produced.
	if result.ArtifactPath == "" {
		t.Fatalf("artifact path empty on error")
	}
	if _, statErr := os.Stat(result.ArtifactPath); statErr != nil {
		t.Fatalf("artifact missing: %v", statErr)
	}

	_, m := extractArtifact(t, result.ArtifactPath)
	provs, ok := m["extraction_provenance"].([]any)
	if !ok {
		t.Fatalf("extraction_provenance not a slice: %T", m["extraction_provenance"])
	}
	foundErr := false
	for _, p := range provs {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if pm["connector"] != "stub" {
			continue
		}
		errs, ok := pm["errors"].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := errs["something"]; ok && v == "stub-failure" {
			foundErr = true
		}
	}
	if !foundErr {
		t.Errorf("manifest extraction_provenance did not carry the stub error: %v", provs)
	}
}

func TestRun_CloneFailure_OtherReposExtract(t *testing.T) {
	// Two repos: one clones successfully and runs the stub connector; one has
	// no insteadOf mapping so git.Clone fails. Verify that the successful
	// repo is fully extracted and the failed clone appears in provenance.
	good := "owner/fixture"
	bad := "owner/missing"
	bare := setupFakeRemote(t)
	installInsteadOf(t, map[string]string{good: bare})

	stub := &stubConnector{name: "stub", commits: 2}

	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
			Raw:   "2025-01-01..2025-06-30",
		},
		Teams: map[string][]string{"platform": {good, bad}},
	}
	out := filepath.Join(t.TempDir(), "artifact.tar.gz")
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		Connectors:  []connector.Connector{stub},
	}

	result, err := run.Run(context.Background(), cfg, opts)
	if !errors.Is(err, run.ErrPartial) {
		t.Fatalf("Run err = %v, want ErrPartial", err)
	}
	if result.ArtifactPath == "" {
		t.Fatal("artifact path empty")
	}
	if _, statErr := os.Stat(result.ArtifactPath); statErr != nil {
		t.Fatalf("artifact missing: %v", statErr)
	}

	dir, m := extractArtifact(t, result.ArtifactPath)

	// Good repo: commits must be present in SQLite.
	db, err := sql.Open("sqlite", filepath.Join(dir, "metrics.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	var commitCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM commits WHERE repo = ?`, good).Scan(&commitCount); err != nil {
		t.Fatalf("count commits: %v", err)
	}
	if commitCount != stub.commits {
		t.Errorf("commits for good repo: got %d want %d", commitCount, stub.commits)
	}

	// Provenance: good repo has a stub entry; bad repo has a clone error entry.
	provs, ok := m["extraction_provenance"].([]any)
	if !ok {
		t.Fatalf("extraction_provenance not a slice: %T", m["extraction_provenance"])
	}
	var foundGoodStub, foundBadClone bool
	for _, p := range provs {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		switch pm["connector"] {
		case "stub":
			if pm["repo"] == good {
				foundGoodStub = true
			}
		case "clone":
			if pm["repo"] == bad {
				if errs, ok := pm["errors"].(map[string]any); ok {
					if _, hasErr := errs["clone"]; hasErr {
						foundBadClone = true
					}
				}
			}
		}
	}
	if !foundGoodStub {
		t.Errorf("no stub provenance entry for %s: %v", good, provs)
	}
	if !foundBadClone {
		t.Errorf("no clone-error provenance entry for %s: %v", bad, provs)
	}
}

// cancelingConnector inserts a fixed number of commits and then cancels the
// run context, simulating a SIGTERM that lands right after a connector's work
// is on disk but before postprocess/finalize. It exercises the partial-artifact
// path (#183): the already-extracted rows must survive into the archived
// metrics.sqlite even though the run never completed.
type cancelingConnector struct {
	name    string
	commits int
	cancel  context.CancelFunc
}

func (c *cancelingConnector) Name() string                   { return c.name }
func (c *cancelingConnector) Ping(ctx context.Context) error { return nil }
func (c *cancelingConnector) Extract(ctx context.Context, repo connector.Repo, w connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.name, repo.Slug, w)
	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < c.commits; i++ {
		sha := fmt.Sprintf("%040x", i+1)
		if err := sink.InsertCommit(model.Commit{
			SHA:         sha,
			Repo:        repo.Slug,
			AuthoredAt:  base.Add(time.Duration(i) * time.Hour),
			CommittedAt: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			prov.Errors[fmt.Sprintf("commit_%d", i)] = err.Error()
			return prov
		}
		prov.RowsReturned["commits"]++
	}
	// Simulate the signal arriving after this connector's rows are committed.
	c.cancel()
	return prov
}

var _ connector.Connector = (*cancelingConnector)(nil)

func TestRun_PartialArtifactAfterExtract(t *testing.T) {
	slug := "owner/fixture"
	bare := setupFakeRemote(t)
	installInsteadOf(t, map[string]string{slug: bare})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &cancelingConnector{name: "stub", commits: 3, cancel: cancel}

	cfg := standardCfg(slug)
	out := filepath.Join(t.TempDir(), "artifact.tar.gz")

	var capturedTmpDir string
	opts := run.Options{
		Out:         out,
		Workers:     1,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		Connectors:  []connector.Connector{conn},
		OnTempDir:   func(p string) { capturedTmpDir = p },
	}

	result, err := run.Run(ctx, cfg, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if !result.Interrupted {
		t.Errorf("Result.Interrupted = false, want true")
	}
	if result.ArtifactPath == "" {
		t.Fatalf("Result.ArtifactPath empty; partial artifact not written (#183)")
	}
	if _, statErr := os.Stat(out); statErr != nil {
		t.Fatalf("partial artifact missing: %v", statErr)
	}

	dir, m := extractArtifact(t, out)

	// The crux of the fix: the connector's already-committed rows survive into
	// the archived metrics.sqlite (WAL folded in before packaging).
	db, err := sql.Open("sqlite", filepath.Join(dir, "metrics.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var commitCount int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM commits WHERE repo = ?`, slug).Scan(&commitCount); err != nil {
		t.Fatalf("count commits: %v", err)
	}
	if commitCount != conn.commits {
		t.Errorf("partial artifact commits = %d, want %d", commitCount, conn.commits)
	}

	// The manifest is marked aborted with a zero completion time.
	if m["aborted"] != true {
		t.Errorf("manifest aborted = %v, want true", m["aborted"])
	}
	if rc, _ := m["run_completed_at"].(string); rc != "0001-01-01T00:00:00Z" {
		t.Errorf("run_completed_at = %q, want zero time on aborted run", rc)
	}

	// Temp dir cleaned after the artifact was written.
	if _, statErr := os.Stat(capturedTmpDir); statErr == nil {
		t.Errorf("temp dir %s not cleaned up after partial finalize", capturedTmpDir)
	}
}

// Compile-time assertion that stubConnector implements connector.Connector.
var _ connector.Connector = (*stubConnector)(nil)
