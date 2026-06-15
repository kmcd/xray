package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/kmcd/xray/internal/model"
)

// batchChunk is the in-memory buffer size before a batch flushes its rows in
// a single explicit transaction. Bounds peak memory across all open batches:
// at ~200 bytes per row and ~13 hot tables open concurrently, < 10 MB.
const batchChunk = 1000

// errBatchClosed is returned from Add/Commit on a batch that has already
// flushed an error or been explicitly rolled back. Subsequent calls are
// observable no-ops (Commit returns the rows committed before the error,
// Add returns errBatchClosed without buffering).
var errBatchClosed = errors.New("store: batch closed")

// batchCore holds the bookkeeping shared by every typed *FooBatch. Per-table
// wrappers embed it and expose typed Add(row) methods.
type batchCore struct {
	store     *Store
	committed int
	err       error // first error wins; sticks once set
	closed    bool
}

// closeWithErr records err (first-wins) and marks the batch closed.
func (b *batchCore) closeWithErr(err error) {
	if err != nil && b.err == nil {
		b.err = err
	}
	b.closed = true
}

// Rollback marks the batch closed without flushing. No-op once Commit() has
// run successfully (closed already true). Safe to call via defer.
func (b *batchCore) Rollback() {
	b.closed = true
}

// runFlush executes one flush under the store's write mutex. The mutex is
// dropped before runFlush returns, so the caller's goroutine releases the
// single SQLite connection between chunks. Other per-row Insert* calls
// from inside the same connector loop (e.g. emitDefects) can therefore
// proceed without deadlocking on a held connection — flushes are atomic
// to the chunk, not to the lifetime of the batch.
func (b *batchCore) runFlush(insertSQL string, exec func(*sql.Stmt) error) error {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()

	tx, err := b.store.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin batch tx: %w", err)
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: prepare batch stmt: %w", err)
	}
	if err := exec(stmt); err != nil {
		_ = stmt.Close()
		_ = tx.Rollback()
		return err
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: close batch stmt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit batch tx: %w", err)
	}
	return nil
}

// --- typed batches ---

// Each *FooBatch:
//   - buffers rows in a slice
//   - auto-flushes when len(buf) >= batchChunk
//   - flush opens an explicit tx, prepares one stmt against the tx, Execs
//     every buffered row, Commits, increments b.committed by the flushed
//     count, and clears the buffer.
//   - on Exec error inside flush: rollback the tx (whole chunk lost),
//     record err, mark closed. The connector logs prov.Errors[<context>]
//     when its loop sees the err return.
//   - Commit() does the tail flush and returns (b.committed, b.err).

// CommitsBatch buffers model.Commit rows for the commits table.
type CommitsBatch struct {
	batchCore
	buf []model.Commit
}

// BeginCommitsBatch opens a new buffered batch for the commits table. The
// returned handle must have Commit() (or Rollback()) called by the caller;
// defer Rollback() is safe and idempotent.
func (s *Store) BeginCommitsBatch() (*CommitsBatch, error) {
	return &CommitsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *CommitsBatch) Add(c model.Commit) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, c)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *CommitsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *CommitsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR REPLACE INTO commits (sha, repo, author_handle, committer_handle, authored_at, committed_at, additions, deletions, files_changed, message_subject, author_is_bot, committer_is_bot, signature_verified, landed_via_pr, reverts_sha, is_revert, is_merge, has_hotfix_marker) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, c := range rows {
			if _, err := stmt.Exec(
				c.SHA, c.Repo, nstr(c.AuthorHandle), nstr(c.CommitterHandle),
				rfc(c.AuthoredAt), rfc(c.CommittedAt),
				c.Additions, c.Deletions, c.FilesChanged, nstr(c.MessageSubject),
				b2i(c.AuthorIsBot), b2i(c.CommitterIsBot),
				nbool(c.SignatureVerified), nbool(c.LandedViaPR),
				nstr(c.RevertsSHA), b2i(c.IsRevert), b2i(c.IsMerge), b2i(c.HasHotfixMarker),
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// CommitFilesBatch buffers model.CommitFile rows for the commit_files table.
type CommitFilesBatch struct {
	batchCore
	buf []model.CommitFile
}

func (s *Store) BeginCommitFilesBatch() (*CommitFilesBatch, error) {
	return &CommitFilesBatch{batchCore: batchCore{store: s}}, nil
}

func (b *CommitFilesBatch) Add(c model.CommitFile) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, c)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *CommitFilesBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *CommitFilesBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT INTO commit_files (commit_sha, repo, path, additions, deletions, change_type, prev_path) VALUES (?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, c := range rows {
			if _, err := stmt.Exec(
				c.CommitSHA, c.Repo, c.Path, c.Additions, c.Deletions,
				c.ChangeType, nstr(c.PrevPath),
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// CommitCoauthorsBatch buffers model.CommitCoauthor rows.
type CommitCoauthorsBatch struct {
	batchCore
	buf []model.CommitCoauthor
}

func (s *Store) BeginCommitCoauthorsBatch() (*CommitCoauthorsBatch, error) {
	return &CommitCoauthorsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *CommitCoauthorsBatch) Add(c model.CommitCoauthor) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, c)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *CommitCoauthorsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *CommitCoauthorsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR REPLACE INTO commit_coauthors (commit_sha, repo, handle, source, kind) VALUES (?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, c := range rows {
			if _, err := stmt.Exec(c.CommitSHA, c.Repo, c.Handle, c.Source, c.Kind); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// PRsBatch buffers model.PR rows for the prs table.
type PRsBatch struct {
	batchCore
	buf []model.PR
}

func (s *Store) BeginPRsBatch() (*PRsBatch, error) {
	return &PRsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *PRsBatch) Add(p model.PR) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, p)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *PRsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *PRsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR REPLACE INTO prs (number, repo, title, opened_at, merged_at, closed_at, author_handle, additions, deletions, files_changed, base_branch, head_sha, merge_sha, merge_method, is_draft, ready_for_review_at, first_review_at, commit_count, head_repo, force_pushed_after_review, body_length, template_match, checklist_total, checklist_checked, has_risk_marker, code_block_count, image_count, link_count, issue_refs_count) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, p := range rows {
			if _, err := stmt.Exec(
				p.Number, p.Repo, nstr(p.Title), rfc(p.OpenedAt),
				nrfc(p.MergedAt), nrfc(p.ClosedAt), nstr(p.AuthorHandle),
				p.Additions, p.Deletions, p.FilesChanged,
				nstr(p.BaseBranch), nstr(p.HeadSHA), nstr(p.MergeSHA), nstr(p.MergeMethod),
				b2i(p.IsDraft), nrfc(p.ReadyForReviewAt), nrfc(p.FirstReviewAt),
				p.CommitCount, nstr(p.HeadRepo), b2i(p.ForcePushedAfterReview),
				p.BodyLength, nfloat(p.TemplateMatch),
				p.ChecklistTotal, p.ChecklistChecked, b2i(p.HasRiskMarker),
				p.CodeBlockCount, p.ImageCount, p.LinkCount, p.IssueRefsCount,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// PRCommitsBatch buffers model.PRCommit rows.
type PRCommitsBatch struct {
	batchCore
	buf []model.PRCommit
}

func (s *Store) BeginPRCommitsBatch() (*PRCommitsBatch, error) {
	return &PRCommitsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *PRCommitsBatch) Add(p model.PRCommit) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, p)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *PRCommitsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *PRCommitsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR IGNORE INTO pr_commits (pr_number, repo, sha) VALUES (?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, p := range rows {
			if _, err := stmt.Exec(p.PRNumber, p.Repo, p.SHA); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// ReviewsBatch buffers model.Review rows.
type ReviewsBatch struct {
	batchCore
	buf []model.Review
}

func (s *Store) BeginReviewsBatch() (*ReviewsBatch, error) {
	return &ReviewsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *ReviewsBatch) Add(r model.Review) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, r)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *ReviewsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *ReviewsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT INTO reviews (pr_number, repo, reviewer_handle, submitted_at, state, body_length) VALUES (?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, r := range rows {
			if _, err := stmt.Exec(r.PRNumber, r.Repo, nstr(r.ReviewerHandle), rfc(r.SubmittedAt), r.State, r.BodyLength); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// PRCommentsBatch buffers model.PRComment rows.
type PRCommentsBatch struct {
	batchCore
	buf []model.PRComment
}

func (s *Store) BeginPRCommentsBatch() (*PRCommentsBatch, error) {
	return &PRCommentsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *PRCommentsBatch) Add(c model.PRComment) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, c)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *PRCommentsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *PRCommentsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT INTO pr_comments (pr_number, repo, author_handle, author_is_bot, created_at, kind, body_length, in_reply_to, path) VALUES (?,?,?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, c := range rows {
			if _, err := stmt.Exec(
				c.PRNumber, c.Repo, nstr(c.AuthorHandle), b2i(c.AuthorIsBot),
				rfc(c.CreatedAt), c.Kind, c.BodyLength, nint64(c.InReplyTo), nstr(c.Path),
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// PRLabelsBatch buffers model.PRLabel rows.
type PRLabelsBatch struct {
	batchCore
	buf []model.PRLabel
}

func (s *Store) BeginPRLabelsBatch() (*PRLabelsBatch, error) {
	return &PRLabelsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *PRLabelsBatch) Add(l model.PRLabel) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, l)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *PRLabelsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *PRLabelsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR IGNORE INTO pr_labels (pr_number, repo, label) VALUES (?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, l := range rows {
			if _, err := stmt.Exec(l.PRNumber, l.Repo, l.Label); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// BuildsBatch buffers model.Build rows.
type BuildsBatch struct {
	batchCore
	buf []model.Build
}

func (s *Store) BeginBuildsBatch() (*BuildsBatch, error) {
	return &BuildsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *BuildsBatch) Add(bd model.Build) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, bd)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *BuildsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *BuildsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR REPLACE INTO builds (id, repo, source, pipeline, status, conclusion, started_at, completed_at, duration_seconds, commit_sha, branch, event, attempt, rerun_of_id, created_at, pr_number) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, bd := range rows {
			if _, err := stmt.Exec(
				bd.ID, bd.Repo, bd.Source, nstr(bd.Pipeline), nstr(bd.Status), nstr(bd.Conclusion),
				nrfc(bd.StartedAt), nrfc(bd.CompletedAt), nint(bd.DurationSeconds),
				nstr(bd.CommitSHA), nstr(bd.Branch), nstr(bd.Event),
				bd.Attempt, nstr(bd.RerunOfID), rfc(bd.CreatedAt), nint(bd.PRNumber),
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// BuildJobsBatch buffers model.BuildJob rows.
type BuildJobsBatch struct {
	batchCore
	buf []model.BuildJob
}

func (s *Store) BeginBuildJobsBatch() (*BuildJobsBatch, error) {
	return &BuildJobsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *BuildJobsBatch) Add(j model.BuildJob) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, j)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *BuildJobsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *BuildJobsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT INTO build_jobs (build_id, repo, name, status, conclusion, duration_seconds, attempt) VALUES (?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, j := range rows {
			if _, err := stmt.Exec(
				j.BuildID, j.Repo, j.Name, nstr(j.Status), nstr(j.Conclusion),
				nint(j.DurationSeconds), j.Attempt,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// FileMetricsBatch buffers model.FileMetric rows.
type FileMetricsBatch struct {
	batchCore
	buf []model.FileMetric
}

func (s *Store) BeginFileMetricsBatch() (*FileMetricsBatch, error) {
	return &FileMetricsBatch{batchCore: batchCore{store: s}}, nil
}

func (b *FileMetricsBatch) Add(f model.FileMetric) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, f)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *FileMetricsBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *FileMetricsBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR REPLACE INTO file_metrics (repo, path, snapshot_sha, language, is_binary, is_generated, is_vendored, is_test, is_dependency_manifest, size_bytes, loc_total, loc_code, loc_blank, max_indent, mean_indent, max_line_length, p95_line_length) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, f := range rows {
			if _, err := stmt.Exec(
				f.Repo, f.Path, f.SnapshotSHA, nstr(f.Language),
				b2i(f.IsBinary), b2i(f.IsGenerated), b2i(f.IsVendored),
				b2i(f.IsTest), b2i(f.IsDependencyManifest),
				f.SizeBytes, f.LOCTotal, f.LOCCode, f.LOCBlank,
				f.MaxIndent, f.MeanIndent, f.MaxLineLength, f.P95LineLength,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// FileComplexityHistoryBatch buffers model.FileComplexityHistory rows for
// the largest hot table.
type FileComplexityHistoryBatch struct {
	batchCore
	buf []model.FileComplexityHistory
}

func (s *Store) BeginFileComplexityHistoryBatch() (*FileComplexityHistoryBatch, error) {
	return &FileComplexityHistoryBatch{batchCore: batchCore{store: s}}, nil
}

func (b *FileComplexityHistoryBatch) Add(f model.FileComplexityHistory) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, f)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *FileComplexityHistoryBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *FileComplexityHistoryBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR REPLACE INTO file_complexity_history (commit_sha, repo, path, n, indent_total, indent_mean, indent_sd, indent_max) VALUES (?,?,?,?,?,?,?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, f := range rows {
			if _, err := stmt.Exec(
				f.CommitSHA, f.Repo, f.Path,
				f.N, f.IndentTotal, f.IndentMean, f.IndentSD, f.IndentMax,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}

// RepoFilesBatch buffers model.RepoFile rows.
type RepoFilesBatch struct {
	batchCore
	buf []model.RepoFile
}

func (s *Store) BeginRepoFilesBatch() (*RepoFilesBatch, error) {
	return &RepoFilesBatch{batchCore: batchCore{store: s}}, nil
}

func (b *RepoFilesBatch) Add(f model.RepoFile) error {
	if b.closed {
		return errBatchClosed
	}
	b.buf = append(b.buf, f)
	if len(b.buf) >= batchChunk {
		return b.flush()
	}
	return nil
}

func (b *RepoFilesBatch) Commit() (int, error) {
	if !b.closed && len(b.buf) > 0 {
		_ = b.flush()
	}
	b.closed = true
	return b.committed, b.err
}

func (b *RepoFilesBatch) flush() error {
	rows := b.buf
	const sqlText = `INSERT OR IGNORE INTO repo_file (repo, path) VALUES (?,?)`
	err := b.runFlush(sqlText, func(stmt *sql.Stmt) error {
		for _, f := range rows {
			if _, err := stmt.Exec(f.Repo, f.Path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.closeWithErr(err)
		b.buf = nil
		return err
	}
	b.committed += len(rows)
	b.buf = b.buf[:0]
	return nil
}
