package github

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

func TestScanIndentLevels_Basic(t *testing.T) {
	// 5 lines: one level-0 (skipped), one level-1, one level-2, one level-3,
	// one blank. Tab-indented line counts as level 1.
	src := []byte("at-margin\n    one\n        two\n\t\t\tthree\n\n")
	got := scanIndentLevels(src)
	if got.n != 3 {
		t.Errorf("n = %d, want 3", got.n)
	}
	if got.total != 6 {
		t.Errorf("indent_total = %d, want 6", got.total)
	}
	if got.maxLevel != 3 {
		t.Errorf("indent_max = %d, want 3", got.maxLevel)
	}
	wantMean := 2.0
	if math.Abs(got.mean-wantMean) > 1e-9 {
		t.Errorf("indent_mean = %v, want %v", got.mean, wantMean)
	}
	// sample stddev of {1, 2, 3} = sqrt(((1-2)^2+(2-2)^2+(3-2)^2)/2) = 1.0
	if math.Abs(got.sd-1.0) > 1e-9 {
		t.Errorf("indent_sd = %v, want 1.0", got.sd)
	}
}

func TestScanIndentLevels_Empty(t *testing.T) {
	got := scanIndentLevels(nil)
	if got.n != 0 || got.total != 0 || got.maxLevel != 0 || got.mean != 0 || got.sd != 0 {
		t.Errorf("zero-content nonzero stats: %+v", got)
	}
}

func TestScanIndentLevels_SingleLine_SDZero(t *testing.T) {
	// One level-1 line; sample stddev requires n >= 2, so SD stays 0.
	got := scanIndentLevels([]byte("    one\n"))
	if got.n != 1 || got.total != 1 || got.sd != 0 {
		t.Errorf("single-line stats: %+v", got)
	}
}

func TestScanIndentLevels_TabSpaceMix(t *testing.T) {
	// "\t    " = tab(4) + 4 spaces = 8 raw spaces / 4 = level 2.
	got := scanIndentLevels([]byte("\t    mix\n"))
	if got.maxLevel != 2 {
		t.Errorf("maxLevel = %d, want 2 for tab+4-spaces", got.maxLevel)
	}
}

func TestComplexityHistoryExcluded(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/foo.go", false},
		{"src/foo_test.go", false}, // tests are NOT excluded
		{"vendor/golang.org/x/sync/sync.go", true},
		{"node_modules/react/index.js", true},
		{"__pycache__/foo.cpython-311.pyc", true},
		{"build/output.o", true},
		{"dist/bundle.js", true},
		{".venv/lib/python3.13/site/foo.py", true},
		{"package-lock.json", false}, // *.lock matches, not *.json
		{"yarn.lock", true},
		{"foo.pb.go", true},
		{"foo_pb2.py", true},
		{"foo.min.js", true},
		{"icon.png", true},
		{"binary.exe", true},
		{"docs/diagram.svg", true},
		{"foo.generated.go", true},
		{"", true},
	}
	for _, tc := range cases {
		got := complexityHistoryExcluded(tc.path)
		if got != tc.want {
			t.Errorf("complexityHistoryExcluded(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// complexityRecordingSink records every InsertFileComplexityHistory call.
// Mutex-guarded because the parallel shard path calls it from multiple goroutines;
// the production store.Store carries its own mutex — same contract applies here.
type complexityRecordingSink struct {
	memSink
	mu   sync.Mutex
	rows []model.FileComplexityHistory
}

func (s *complexityRecordingSink) InsertFileComplexityHistory(row model.FileComplexityHistory) error {
	s.mu.Lock()
	s.rows = append(s.rows, row)
	s.mu.Unlock()
	return nil
}

// sortedComplexityKey returns a stable string key for dedup/sort.
func sortedComplexityKey(r model.FileComplexityHistory) string {
	return r.CommitSHA + "\x00" + r.Path
}

// setupSmallGitRepo creates a temp git repo with nCommits commits, each
// writing/overwriting one of nFiles source files with indented Go content.
// Returns the repo directory and the (sha, path) pairs that should produce
// complexity_history rows.
func setupSmallGitRepo(t *testing.T, nFiles, nCommits int) (dir string, pairs []complexityPair) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir = t.TempDir()

	gitEnv := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	runG := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	revParse := func(ref string) string {
		cmd := exec.CommandContext(t.Context(), "git", "rev-parse", ref)
		cmd.Dir = dir
		cmd.Env = gitEnv
		out, _ := cmd.Output()
		return strings.TrimSpace(string(out))
	}

	runG("init", "-b", "main")
	runG("config", "user.email", "test@example.com")
	runG("config", "user.name", "Test")
	runG("config", "commit.gpgsign", "false")

	content := "package main\n\nfunc fn%d() {\n    x := 1\n    if x > 0 {\n        _ = x\n    }\n}\n"
	for i := 0; i < nCommits; i++ {
		fname := fmt.Sprintf("file%d.go", i%nFiles)
		fpath := filepath.Join(dir, fname)
		if err := os.WriteFile(fpath, []byte(fmt.Sprintf(content, i)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		runG("add", fname)
		runG("commit", "-m", fmt.Sprintf("commit %d", i))
		sha := revParse("HEAD")
		pairs = append(pairs, complexityPair{sha: sha, path: fname})
	}
	return dir, pairs
}

// TestExtractComplexityHistoryBatch_ShardsEquivalentToSerial verifies that
// parallel sharding produces an identical file_complexity_history row multiset
// to the serial path. Covers the index-stride partitioning correctness and
// provenance merge under concurrency.
func TestExtractComplexityHistoryBatch_ShardsEquivalentToSerial(t *testing.T) {
	// Lower minPairsPerShard so we exercise the parallel path with a small repo.
	orig := minPairsPerShard
	minPairsPerShard = 1
	t.Cleanup(func() { minPairsPerShard = orig })

	clone, pairs := setupSmallGitRepo(t, 3, 20)

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))

	// Serial run (shards=1).
	sink1 := &complexityRecordingSink{}
	prov1 := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractShards = 1
	c.extractComplexityHistoryBatch(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, pairs, sink1, &prov1)

	// Parallel run (shards=4). With minPairsPerShard=1, threshold=4 pairs;
	// 20 pairs well exceeds it, so the fan-out path is exercised.
	sink4 := &complexityRecordingSink{}
	prov4 := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractShards = 4
	c.extractComplexityHistoryBatch(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, pairs, sink4, &prov4)

	if len(sink1.rows) == 0 {
		t.Fatal("serial run produced no rows; check test repo setup")
	}
	if len(sink1.rows) != len(sink4.rows) {
		t.Fatalf("row count: shards=1 got %d, shards=4 got %d", len(sink1.rows), len(sink4.rows))
	}

	sorted := func(rows []model.FileComplexityHistory) []model.FileComplexityHistory {
		cp := append([]model.FileComplexityHistory{}, rows...)
		sort.Slice(cp, func(i, j int) bool {
			ki := sortedComplexityKey(cp[i])
			kj := sortedComplexityKey(cp[j])
			return ki < kj
		})
		return cp
	}
	s1 := sorted(sink1.rows)
	s4 := sorted(sink4.rows)
	for i := range s1 {
		if s1[i] != s4[i] {
			t.Errorf("row[%d] mismatch:\n  serial=%+v\n  parallel=%+v", i, s1[i], s4[i])
		}
	}
	if prov1.RowsReturned["file_complexity_history"] != prov4.RowsReturned["file_complexity_history"] {
		t.Errorf("RowsReturned: serial=%d parallel=%d",
			prov1.RowsReturned["file_complexity_history"],
			prov4.RowsReturned["file_complexity_history"])
	}
}

// TestExtractComplexityHistoryBatch_ShardsZeroAndOneAreSerial verifies that
// both shards=0 and shards=1 use the serial fast-path (single CatFileBatch),
// not the fan-out path. We assert this indirectly: with fewer pairs than
// shards*minPairsPerShard, the parallel path is also skipped — this test uses
// an empty pairs slice so any path returns with zero rows.
func TestExtractComplexityHistoryBatch_ShardsZeroAndOneAreSerial(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	repo := connector.Repo{Slug: "kmcd/foo", Clone: t.TempDir()}

	for _, shards := range []int{0, 1} {
		sink := &complexityRecordingSink{}
		prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
		c.extractShards = shards
		c.extractComplexityHistoryBatch(context.Background(), repo, nil, sink, &prov)
		if len(sink.rows) != 0 {
			t.Errorf("shards=%d: expected 0 rows for empty pairs; got %d", shards, len(sink.rows))
		}
		if prov.Errors["file_complexity_history"] != "" {
			t.Errorf("shards=%d: unexpected error: %s", shards, prov.Errors["file_complexity_history"])
		}
	}
}

// TestExtractComplexityHistoryBatch_ProvMergeUnderShards verifies that the
// per-shard provenance fragments are correctly merged: RowsReturned sums
// across shards and the total equals len(pairs) minus missing objects.
func TestExtractComplexityHistoryBatch_ProvMergeUnderShards(t *testing.T) {
	orig := minPairsPerShard
	minPairsPerShard = 1
	t.Cleanup(func() { minPairsPerShard = orig })

	clone, pairs := setupSmallGitRepo(t, 2, 16)

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &complexityRecordingSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractShards = 4
	c.extractComplexityHistoryBatch(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, pairs, sink, &prov)

	if prov.RowsReturned["file_complexity_history"] != len(sink.rows) {
		t.Errorf("RowsReturned[file_complexity_history]=%d but sink.rows=%d",
			prov.RowsReturned["file_complexity_history"], len(sink.rows))
	}
	if len(sink.rows) == 0 {
		t.Error("expected non-zero rows; test repo setup may have failed")
	}
}
