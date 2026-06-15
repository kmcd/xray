package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/store"
)

// TestBatchCommitsSuccess flushes 2500 commits (two full chunks + a 500-row
// tail) and asserts both the in-flight committed counter and the on-disk
// row count agree.
func TestBatchCommitsSuccess(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.sqlite")
	st, err := store.Open(dbPath, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	batch, err := st.BeginCommitsBatch()
	if err != nil {
		t.Fatalf("BeginCommitsBatch: %v", err)
	}
	defer batch.Rollback()

	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	const n = 2500
	for i := 0; i < n; i++ {
		if err := batch.Add(model.Commit{
			SHA:            fmt.Sprintf("sha%05d", i),
			Repo:           "kmcd/foo",
			AuthorHandle:   "h_1",
			AuthoredAt:     now,
			CommittedAt:    now,
			MessageSubject: "x",
		}); err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
	}
	committed, err := batch.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if committed != n {
		t.Errorf("committed = %d, want %d", committed, n)
	}

	got := countRows(t, dbPath, "commits")
	if got != n {
		t.Errorf("COUNT(*) = %d, want %d", got, n)
	}
}

// TestBatchCommitsRollbackOnExecError verifies that an error inside a flush
// rolls back that chunk's tx atomically: rows from the failed chunk are
// discarded, prior fully-flushed chunks survive, and the batch closes. We
// trigger the in-flush failure by closing the underlying DB after the first
// chunk has committed; subsequent Begin observes "database is closed".
func TestBatchCommitsRollbackOnExecError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.sqlite")
	st, err := store.Open(dbPath, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	batch, err := st.BeginCommitsBatch()
	if err != nil {
		t.Fatalf("BeginCommitsBatch: %v", err)
	}
	defer batch.Rollback()

	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	// Add 1000 rows — auto-flushes at row 999. The first chunk lands on disk.
	for i := 0; i < 1000; i++ {
		if err := batch.Add(model.Commit{
			SHA: fmt.Sprintf("sha%05d", i), Repo: "x",
			AuthoredAt: now, CommittedAt: now, MessageSubject: "y",
		}); err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
	}
	// First-chunk commit landed. Verify.
	if got := countRows(t, dbPath, "commits"); got != 1000 {
		t.Fatalf("after first flush COUNT(*) = %d, want 1000", got)
	}

	// Buffer a tail row, then close the store. The Commit() flush will fail
	// when it tries to Begin a tx against the closed DB. The tail row's
	// chunk should not appear in the on-disk count, and Commit's err is
	// non-nil.
	if err := batch.Add(model.Commit{
		SHA: "tail", Repo: "x",
		AuthoredAt: now, CommittedAt: now, MessageSubject: "z",
	}); err != nil {
		t.Fatalf("Add tail: %v", err)
	}
	// Close the store: subsequent batch.Commit() flush errors on Begin.
	if err := st.Close(); err != nil {
		t.Fatalf("st.Close: %v", err)
	}

	committed, finalErr := batch.Commit()
	if finalErr == nil {
		t.Fatalf("Commit after store.Close() should return non-nil err")
	}
	if committed != 1000 {
		t.Errorf("committed after failed final flush = %d, want 1000", committed)
	}
	got := countRows(t, dbPath, "commits")
	if got != 1000 {
		t.Errorf("commits COUNT(*) = %d, want 1000 (tail chunk lost)", got)
	}

	// A subsequent Add on the closed batch returns non-nil (closed sentinel).
	if err := batch.Add(model.Commit{SHA: "later", Repo: "x", AuthoredAt: now, CommittedAt: now, MessageSubject: "w"}); err == nil {
		t.Errorf("Add on closed batch returned nil, want non-nil")
	}
}

// TestBatchConcurrentSerialisation runs two goroutines, each opening their
// own batch and Adding 500 rows. Both must succeed; total row count = 1000.
// The store's mutex serialises flushes; modernc/sqlite's single-conn pool
// serialises writes. We assert correctness (no row loss) — the test does not
// directly inspect concurrent open count.
func TestBatchConcurrentSerialisation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.sqlite")
	st, err := store.Open(dbPath, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

	var wg sync.WaitGroup
	var totalCommitted int64
	for g := 0; g < 2; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch, err := st.BeginCommitsBatch()
			if err != nil {
				t.Errorf("BeginCommitsBatch[g=%d]: %v", g, err)
				return
			}
			defer batch.Rollback()
			for i := 0; i < 500; i++ {
				if err := batch.Add(model.Commit{
					SHA:            fmt.Sprintf("g%d-%05d", g, i),
					Repo:           "kmcd/foo",
					AuthoredAt:     now,
					CommittedAt:    now,
					MessageSubject: "x",
				}); err != nil {
					t.Errorf("Add[g=%d,i=%d]: %v", g, i, err)
					return
				}
			}
			committed, err := batch.Commit()
			if err != nil {
				t.Errorf("Commit[g=%d]: %v", g, err)
				return
			}
			atomic.AddInt64(&totalCommitted, int64(committed))
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&totalCommitted); got != 1000 {
		t.Errorf("totalCommitted = %d, want 1000", got)
	}
	if got := countRows(t, dbPath, "commits"); got != 1000 {
		t.Errorf("commits COUNT(*) = %d, want 1000", got)
	}
}

// TestBatchRollbackBeforeCommit verifies Rollback on a batch with buffered
// rows discards them (they were never flushed) and closes the batch.
func TestBatchRollbackBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.sqlite")
	st, err := store.Open(dbPath, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	batch, err := st.BeginCommitsBatch()
	if err != nil {
		t.Fatalf("BeginCommitsBatch: %v", err)
	}
	for i := 0; i < 50; i++ {
		if err := batch.Add(model.Commit{
			SHA:            fmt.Sprintf("sha%05d", i),
			Repo:           "x",
			AuthoredAt:     now,
			CommittedAt:    now,
			MessageSubject: "y",
		}); err != nil {
			t.Fatalf("Add[%d]: %v", i, err)
		}
	}
	batch.Rollback()

	// Commit after Rollback observes closed=true; the buffered rows never
	// flushed, so committed = 0 and on-disk count = 0.
	committed, err := batch.Commit()
	if err != nil {
		t.Errorf("Commit after Rollback err = %v, want nil", err)
	}
	if committed != 0 {
		t.Errorf("committed after Rollback = %d, want 0", committed)
	}
	if got := countRows(t, dbPath, "commits"); got != 0 {
		t.Errorf("commits COUNT(*) = %d, want 0", got)
	}

	// A subsequent batch from the same store must succeed.
	batch2, err := st.BeginCommitsBatch()
	if err != nil {
		t.Fatalf("BeginCommitsBatch #2: %v", err)
	}
	defer batch2.Rollback()
	if err := batch2.Add(model.Commit{SHA: "later", Repo: "x", AuthoredAt: now, CommittedAt: now, MessageSubject: "z"}); err != nil {
		t.Fatalf("Add on second batch: %v", err)
	}
	if _, err := batch2.Commit(); err != nil {
		t.Fatalf("Commit on second batch: %v", err)
	}
	if got := countRows(t, dbPath, "commits"); got != 1 {
		t.Errorf("commits COUNT(*) after second batch = %d, want 1", got)
	}
}

// TestBatchInsertOnePerHotTable mirrors TestInsertOnePerTable but exercises
// each Begin*Batch entry point with a single row, asserting at least the
// flush+commit wiring works end-to-end for each hot table.
func TestBatchInsertOnePerHotTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.sqlite")
	st, err := store.Open(dbPath, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	bp := func(b bool) *bool { return &b }
	fp := func(f float64) *float64 { return &f }

	// commits
	cb, err := st.BeginCommitsBatch()
	if err != nil {
		t.Fatalf("BeginCommitsBatch: %v", err)
	}
	if err := cb.Add(model.Commit{
		SHA: "abc", Repo: "kmcd/foo", AuthorHandle: "h_1", CommitterHandle: "h_1",
		AuthoredAt: now, CommittedAt: now, MessageSubject: "init",
		SignatureVerified: bp(true), LandedViaPR: bp(false),
	}); err != nil {
		t.Fatalf("CommitsBatch.Add: %v", err)
	}
	if n, err := cb.Commit(); err != nil || n != 1 {
		t.Errorf("CommitsBatch.Commit: n=%d err=%v", n, err)
	}

	// commit_files
	cfb, err := st.BeginCommitFilesBatch()
	if err != nil {
		t.Fatalf("BeginCommitFilesBatch: %v", err)
	}
	if err := cfb.Add(model.CommitFile{CommitSHA: "abc", Repo: "kmcd/foo", Path: "x.go", Additions: 1, ChangeType: "A"}); err != nil {
		t.Fatalf("CommitFilesBatch.Add: %v", err)
	}
	if n, err := cfb.Commit(); err != nil || n != 1 {
		t.Errorf("CommitFilesBatch.Commit: n=%d err=%v", n, err)
	}

	// commit_coauthors
	ccb, err := st.BeginCommitCoauthorsBatch()
	if err != nil {
		t.Fatalf("BeginCommitCoauthorsBatch: %v", err)
	}
	if err := ccb.Add(model.CommitCoauthor{CommitSHA: "abc", Repo: "kmcd/foo", Handle: "co", Source: "trailer", Kind: "human"}); err != nil {
		t.Fatalf("CommitCoauthorsBatch.Add: %v", err)
	}
	if n, err := ccb.Commit(); err != nil || n != 1 {
		t.Errorf("CommitCoauthorsBatch.Commit: n=%d err=%v", n, err)
	}

	// prs
	pb, err := st.BeginPRsBatch()
	if err != nil {
		t.Fatalf("BeginPRsBatch: %v", err)
	}
	if err := pb.Add(model.PR{Number: 1, Repo: "kmcd/foo", Title: "t", OpenedAt: now, TemplateMatch: fp(1)}); err != nil {
		t.Fatalf("PRsBatch.Add: %v", err)
	}
	if n, err := pb.Commit(); err != nil || n != 1 {
		t.Errorf("PRsBatch.Commit: n=%d err=%v", n, err)
	}

	// pr_commits
	pcb, err := st.BeginPRCommitsBatch()
	if err != nil {
		t.Fatalf("BeginPRCommitsBatch: %v", err)
	}
	if err := pcb.Add(model.PRCommit{PRNumber: 1, Repo: "kmcd/foo", SHA: "abc"}); err != nil {
		t.Fatalf("PRCommitsBatch.Add: %v", err)
	}
	if n, err := pcb.Commit(); err != nil || n != 1 {
		t.Errorf("PRCommitsBatch.Commit: n=%d err=%v", n, err)
	}

	// reviews
	rb, err := st.BeginReviewsBatch()
	if err != nil {
		t.Fatalf("BeginReviewsBatch: %v", err)
	}
	if err := rb.Add(model.Review{PRNumber: 1, Repo: "kmcd/foo", ReviewerHandle: "r", SubmittedAt: now, State: "APPROVED"}); err != nil {
		t.Fatalf("ReviewsBatch.Add: %v", err)
	}
	if n, err := rb.Commit(); err != nil || n != 1 {
		t.Errorf("ReviewsBatch.Commit: n=%d err=%v", n, err)
	}

	// pr_comments
	pcmt, err := st.BeginPRCommentsBatch()
	if err != nil {
		t.Fatalf("BeginPRCommentsBatch: %v", err)
	}
	if err := pcmt.Add(model.PRComment{PRNumber: 1, Repo: "kmcd/foo", AuthorHandle: "a", CreatedAt: now, Kind: "issue_comment"}); err != nil {
		t.Fatalf("PRCommentsBatch.Add: %v", err)
	}
	if n, err := pcmt.Commit(); err != nil || n != 1 {
		t.Errorf("PRCommentsBatch.Commit: n=%d err=%v", n, err)
	}

	// pr_labels
	plb, err := st.BeginPRLabelsBatch()
	if err != nil {
		t.Fatalf("BeginPRLabelsBatch: %v", err)
	}
	if err := plb.Add(model.PRLabel{PRNumber: 1, Repo: "kmcd/foo", Label: "bug"}); err != nil {
		t.Fatalf("PRLabelsBatch.Add: %v", err)
	}
	if n, err := plb.Commit(); err != nil || n != 1 {
		t.Errorf("PRLabelsBatch.Commit: n=%d err=%v", n, err)
	}

	// builds
	bb, err := st.BeginBuildsBatch()
	if err != nil {
		t.Fatalf("BeginBuildsBatch: %v", err)
	}
	dur := 10
	if err := bb.Add(model.Build{ID: "b1", Repo: "kmcd/foo", Source: "github_actions", Status: "ok", CreatedAt: now, DurationSeconds: &dur}); err != nil {
		t.Fatalf("BuildsBatch.Add: %v", err)
	}
	if n, err := bb.Commit(); err != nil || n != 1 {
		t.Errorf("BuildsBatch.Commit: n=%d err=%v", n, err)
	}

	// build_jobs
	bjb, err := st.BeginBuildJobsBatch()
	if err != nil {
		t.Fatalf("BeginBuildJobsBatch: %v", err)
	}
	if err := bjb.Add(model.BuildJob{BuildID: "b1", Repo: "kmcd/foo", Name: "test", Attempt: 1}); err != nil {
		t.Fatalf("BuildJobsBatch.Add: %v", err)
	}
	if n, err := bjb.Commit(); err != nil || n != 1 {
		t.Errorf("BuildJobsBatch.Commit: n=%d err=%v", n, err)
	}

	// file_metrics
	fmb, err := st.BeginFileMetricsBatch()
	if err != nil {
		t.Fatalf("BeginFileMetricsBatch: %v", err)
	}
	if err := fmb.Add(model.FileMetric{Repo: "kmcd/foo", Path: "x.go", SnapshotSHA: "abc", Language: "Go"}); err != nil {
		t.Fatalf("FileMetricsBatch.Add: %v", err)
	}
	if n, err := fmb.Commit(); err != nil || n != 1 {
		t.Errorf("FileMetricsBatch.Commit: n=%d err=%v", n, err)
	}

	// file_complexity_history
	fchb, err := st.BeginFileComplexityHistoryBatch()
	if err != nil {
		t.Fatalf("BeginFileComplexityHistoryBatch: %v", err)
	}
	if err := fchb.Add(model.FileComplexityHistory{CommitSHA: "abc", Repo: "kmcd/foo", Path: "x.go", N: 1, IndentMean: 1.0}); err != nil {
		t.Fatalf("FileComplexityHistoryBatch.Add: %v", err)
	}
	if n, err := fchb.Commit(); err != nil || n != 1 {
		t.Errorf("FileComplexityHistoryBatch.Commit: n=%d err=%v", n, err)
	}

	// repo_file
	rfb, err := st.BeginRepoFilesBatch()
	if err != nil {
		t.Fatalf("BeginRepoFilesBatch: %v", err)
	}
	if err := rfb.Add(model.RepoFile{Repo: "kmcd/foo", Path: "x.go"}); err != nil {
		t.Fatalf("RepoFilesBatch.Add: %v", err)
	}
	if n, err := rfb.Commit(); err != nil || n != 1 {
		t.Errorf("RepoFilesBatch.Commit: n=%d err=%v", n, err)
	}
}

// countRows opens the sqlite at path and returns COUNT(*) FROM the named
// table. Helper local to this _test file.
func countRows(t *testing.T, dbPath, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
