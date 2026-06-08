package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// repoFileSink records InsertRepoFile rows.
type repoFileSink struct {
	memSink
	files []model.RepoFile
}

func (s *repoFileSink) InsertRepoFile(f model.RepoFile) error {
	s.files = append(s.files, f)
	return nil
}

// setupRepoFileFixture creates a git repo with:
//   - src/main.go, src/util.go
//   - .gitignore that excludes build/
//   - build/output (gitignored, should be absent)
//   - a symlink link.go -> src/main.go
//
// Returns the clone path.
func setupRepoFileFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		// #nosec G204 -- args are test-controlled literals.
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	run("config", "commit.gpgsign", "false")

	write(".gitignore", "build/\n")
	write("src/main.go", "package main\n")
	write("src/util.go", "package main\n")
	// gitignored — must not appear in repo_file
	write("build/output", "binary\n")

	// Symlink: link.go -> src/main.go (tracked, not followed)
	if err := os.Symlink("src/main.go", filepath.Join(dir, "link.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	run("add", ".")
	run("commit", "-q", "-m", "init")

	return dir
}

func TestExtractRepoFiles(t *testing.T) {
	clone := setupRepoFileFixture(t)
	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &repoFileSink{}
	repo := connector.Repo{Slug: "kmcd/foo", Clone: clone}
	prov := connector.NewProvenance(c.Name(), repo.Slug, standardWindow())

	c.extractRepoFiles(context.Background(), repo, sink, &prov)

	got := make([]string, len(sink.files))
	for i, f := range sink.files {
		if f.Repo != repo.Slug {
			t.Errorf("row %d: repo = %q, want %q", i, f.Repo, repo.Slug)
		}
		got[i] = f.Path
	}
	sort.Strings(got)

	want := []string{".gitignore", "link.go", "src/main.go", "src/util.go"}
	if len(got) != len(want) {
		t.Fatalf("paths: got %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("path[%d]: got %q, want %q", i, got[i], p)
		}
	}

	// build/output must be absent (gitignored)
	for _, f := range sink.files {
		if strings.HasPrefix(f.Path, "build/") {
			t.Errorf("gitignored path %q should not appear in repo_file", f.Path)
		}
	}

	if prov.RowsReturned["repo_file"] != len(want) {
		t.Errorf("provenance repo_file = %d, want %d", prov.RowsReturned["repo_file"], len(want))
	}
}

func TestExtractRepoFiles_EmptyClone(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()
	c := newTestConnector(t, srv)
	sink := &repoFileSink{}
	repo := connector.Repo{Slug: "kmcd/foo", Clone: ""}
	prov := connector.NewProvenance(c.Name(), repo.Slug, standardWindow())
	c.extractRepoFiles(context.Background(), repo, sink, &prov)
	if len(sink.files) != 0 {
		t.Errorf("expected no rows for empty clone, got %d", len(sink.files))
	}
}

func TestExtractRepoFiles_MultiRepo(t *testing.T) {
	clone1 := setupRepoFileFixture(t)
	clone2 := t.TempDir()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	run2 := func(args ...string) {
		// #nosec G204
		cmd := exec.Command("git", args...)
		cmd.Dir = clone2
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(clone2, "README.md"), []byte("# Repo2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run2("init", "-q", "-b", "main")
	run2("config", "user.email", "t@example.com")
	run2("config", "user.name", "T")
	run2("config", "commit.gpgsign", "false")
	run2("add", ".")
	run2("commit", "-q", "-m", "init")

	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()
	c := newTestConnector(t, srv)

	type result struct {
		slug  string
		paths []string
	}
	var results []result
	for _, tc := range []struct{ slug, clone string }{
		{"org/repo1", clone1},
		{"org/repo2", clone2},
	} {
		sink := &repoFileSink{}
		repo := connector.Repo{Slug: tc.slug, Clone: tc.clone}
		prov := connector.NewProvenance(c.Name(), repo.Slug, standardWindow())
		c.extractRepoFiles(context.Background(), repo, sink, &prov)
		paths := make([]string, len(sink.files))
		for i, f := range sink.files {
			if f.Repo != tc.slug {
				t.Errorf("repo mismatch: got %q, want %q", f.Repo, tc.slug)
			}
			paths[i] = f.Path
		}
		sort.Strings(paths)
		results = append(results, result{tc.slug, paths})
	}

	if len(results[0].paths) == 0 {
		t.Errorf("repo1: expected paths, got none")
	}
	if len(results[1].paths) != 1 || results[1].paths[0] != "README.md" {
		t.Errorf("repo2: got %v, want [README.md]", results[1].paths)
	}
}
