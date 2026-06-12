package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// fileMetricSink records InsertFileMetric calls for walk assertions.
// Mutex-guarded because the parallel walk path calls it from multiple goroutines;
// the production store.Store carries its own mutex — same contract applies here.
type fileMetricSink struct {
	memSink
	mu          sync.Mutex
	fileMetrics []model.FileMetric
	languages   []model.RepoLanguage
}

func (s *fileMetricSink) InsertFileMetric(fm model.FileMetric) error {
	s.mu.Lock()
	s.fileMetrics = append(s.fileMetrics, fm)
	s.mu.Unlock()
	return nil
}
func (s *fileMetricSink) InsertRepoLanguage(l model.RepoLanguage) error {
	s.mu.Lock()
	s.languages = append(s.languages, l)
	s.mu.Unlock()
	return nil
}

// TestIsBinaryByExt covers the binary-extension detection added by lever 3.
func TestIsBinaryByExt(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"image.png", true},
		{"photo.jpg", true},
		{"photo.jpeg", true},
		{"anim.gif", true},
		{"icon.webp", true},
		{"favicon.ico", true},
		{"diagram.svg", true},
		{"doc.pdf", true},
		{"archive.zip", true},
		{"archive.tar", true},
		{"archive.gz", true},
		{"archive.tgz", true},
		{"archive.bz2", true},
		{"archive.7z", true},
		{"lib.jar", true},
		{"app.war", true},
		{"compiled.class", true},
		{"lib.so", true},
		{"lib.dll", true},
		{"lib.dylib", true},
		{"app.exe", true},
		{"data.bin", true},
		{"module.wasm", true},
		{"font.woff", true},
		{"font.woff2", true},
		{"font.ttf", true},
		{"font.eot", true},
		{"font.otf", true},
		{"video.mp4", true},
		{"video.mov", true},
		{"video.webm", true},
		{"audio.mp3", true},
		{"audio.wav", true},
		{"audio.ogg", true},
		{"UPPER.PNG", true},  // case-insensitive
		{"mixed.Jpg", true},
		{"main.go", false},
		{"script.py", false},
		{"README.md", false},
		{"data.json", false},
		{"config.yml", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isBinaryByExt(tc.path)
		if got != tc.want {
			t.Errorf("isBinaryByExt(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestExtractWorkingTree_SkipsReadOnVendored verifies that a file inside a
// vendor/ directory is emitted as a file_metrics row with IsVendored=true and
// zero LOC stats — confirming os.ReadFile was skipped (lever 3).
func TestExtractWorkingTree_SkipsReadOnVendored(t *testing.T) {
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, "vendor", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	vendorContent := "package vendor\n\nfunc Init() {\n    x := 1\n    _ = x\n}\n"
	if err := os.WriteFile(filepath.Join(clone, "vendor", "lib", "foo.go"), []byte(vendorContent), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &fileMetricSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	if len(sink.fileMetrics) != 1 {
		t.Fatalf("expected 1 file_metrics row, got %d", len(sink.fileMetrics))
	}
	row := sink.fileMetrics[0]
	if !row.IsVendored {
		t.Errorf("expected IsVendored=true for vendor/lib/foo.go, got false")
	}
	// LOC fields must be zero: lever 3 skips ReadFile so the line scanner never runs.
	if row.LOCTotal != 0 || row.LOCCode != 0 || row.LOCBlank != 0 {
		t.Errorf("expected zero LOC stats for vendored file (ReadFile skipped), got LOCTotal=%d LOCCode=%d LOCBlank=%d",
			row.LOCTotal, row.LOCCode, row.LOCBlank)
	}
	if prov.RowsReturned["file_metrics"] != 1 {
		t.Errorf("RowsReturned[file_metrics] = %d, want 1", prov.RowsReturned["file_metrics"])
	}
}

// TestExtractWorkingTree_SkipsReadOnBinaryByExtension verifies that a .png
// file with non-PNG content is emitted with IsBinary=true determined from
// its extension (not from content inspection). This proves lever 3 ran before
// any enry.IsBinary check.
func TestExtractWorkingTree_SkipsReadOnBinaryByExtension(t *testing.T) {
	clone := t.TempDir()
	// Write clearly non-binary content but with a .png extension.
	// Without lever 3, enry.IsBinary would return false; with lever 3,
	// IsBinary=true comes from the extension table.
	if err := os.WriteFile(filepath.Join(clone, "image.png"), []byte("definitely not a PNG header"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &fileMetricSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	if len(sink.fileMetrics) != 1 {
		t.Fatalf("expected 1 file_metrics row, got %d", len(sink.fileMetrics))
	}
	row := sink.fileMetrics[0]
	if !row.IsBinary {
		t.Errorf("expected IsBinary=true for .png file from extension table, got false; "+
			"if content was read, enry would have returned false for this text content")
	}
	if row.LOCTotal != 0 || row.LOCCode != 0 {
		t.Errorf("expected zero LOC for binary-ext file (ReadFile skipped), got LOCTotal=%d LOCCode=%d",
			row.LOCTotal, row.LOCCode)
	}
}

// TestExtractWorkingTree_ParallelEquivalentToSerial confirms that
// c.extractShards=4 produces the same file_metrics row count and
// repo_languages row count as c.extractShards=1 for a mixed-type tree.
func TestExtractWorkingTree_ParallelEquivalentToSerial(t *testing.T) {
	clone := t.TempDir()

	files := map[string]string{
		"main.go":   "package main\nfunc main() {\n    fmt.Println(\"hi\")\n}\n",
		"helper.go": "package main\nfunc help() {\n    x := 1\n    _ = x\n}\n",
		"script.py": "def main():\n    print('hi')\n",
		"README.md": "# Hello\nWorld\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(clone, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// vendored file — should be included in metrics but without LOC
	if err := os.MkdirAll(filepath.Join(clone, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "vendor", "dep.go"), []byte("package dep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// binary-ext file
	if err := os.WriteFile(filepath.Join(clone, "logo.png"), []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	repo := connector.Repo{Slug: "kmcd/foo", Clone: clone}

	run := func(shards int) (metrics []model.FileMetric, langs []model.RepoLanguage, prov connector.Provenance) {
		sink := &fileMetricSink{}
		prov = connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
		c.extractShards = shards
		c.extractWorkingTree(context.Background(), repo, standardWindow(), sink, &prov)
		return sink.fileMetrics, sink.languages, prov
	}

	m1, l1, p1 := run(1)
	m4, l4, p4 := run(4)

	if len(m1) != len(m4) {
		t.Errorf("file_metrics rows: serial=%d parallel=%d", len(m1), len(m4))
	}

	// Language totals should be identical (same files, same bytes).
	totalBytes := func(langs []model.RepoLanguage) map[string]int64 {
		out := make(map[string]int64)
		for _, l := range langs {
			out[l.Language] += l.Bytes
		}
		return out
	}
	lb1, lb4 := totalBytes(l1), totalBytes(l4)

	langs := make(map[string]bool)
	for k := range lb1 {
		langs[k] = true
	}
	for k := range lb4 {
		langs[k] = true
	}
	for lang := range langs {
		if lb1[lang] != lb4[lang] {
			t.Errorf("language %q bytes: serial=%d parallel=%d", lang, lb1[lang], lb4[lang])
		}
	}

	if p1.RowsReturned["file_metrics"] != p4.RowsReturned["file_metrics"] {
		t.Errorf("RowsReturned[file_metrics]: serial=%d parallel=%d",
			p1.RowsReturned["file_metrics"], p4.RowsReturned["file_metrics"])
	}
	if p1.RowsReturned["repo_languages"] != p4.RowsReturned["repo_languages"] {
		t.Errorf("RowsReturned[repo_languages]: serial=%d parallel=%d",
			p1.RowsReturned["repo_languages"], p4.RowsReturned["repo_languages"])
	}
}

// TestExtractWorkingTree_ParallelRootError verifies that a root walk error is
// recorded in all four prov.Errors keys for the parallel path, matching the
// serial path's behaviour.
func TestExtractWorkingTree_ParallelRootError(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &fileMetricSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	absentClone := filepath.Join(t.TempDir(), "does-not-exist")
	c.extractShards = 4
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: absentClone}, standardWindow(), sink, &prov)

	for _, key := range []string{"walk", "file_metrics", "harness_artifacts", "repo_languages"} {
		if prov.Errors[key] == "" {
			t.Errorf("parallel: expected prov.Errors[%q] set on root walk failure; got empty", key)
		}
	}
}

// TestExtractWorkingTree_VendoredFileInLanguageTotals verifies that vendored
// files still contribute to repo_languages (by extension fallback) even when
// their content is not read.
func TestExtractWorkingTree_VendoredFileInLanguageTotals(t *testing.T) {
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	vendorContent := "package dep\nfunc Dep() {}\n"
	if err := os.WriteFile(filepath.Join(clone, "vendor", "dep.go"), []byte(vendorContent), 0o644); err != nil {
		t.Fatal(err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &fileMetricSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	// The vendored .go file should contribute to the Go language total via
	// extension fallback (enry.GetLanguageByExtension).
	langBytes := make(map[string]int64)
	for _, l := range sink.languages {
		langBytes[l.Language] += l.Bytes
	}
	if langBytes["Go"] == 0 {
		langs := make([]string, 0, len(langBytes))
		for k := range langBytes {
			langs = append(langs, k)
		}
		sort.Strings(langs)
		t.Errorf("expected Go language total > 0 for vendored .go file via extension fallback; got langs=%v", langs)
	}
}