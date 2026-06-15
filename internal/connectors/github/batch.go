package github

import (
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/store"
)

// This file wires the github connector's hot-row loops to the buffered
// per-table batch handles in internal/store. The connector code threads a
// thin commitsBatchHandle/etc. interface through its loops; at runtime the
// handle is either the real *store.FooBatch (when the sink is the real
// Store) or a per-row shim that wraps sink.InsertFoo (for test fakes that
// only implement connector.Sink).
//
// Why the shim instead of extending connector.Sink with 13 new methods?
// Test fakes (e.g. github/http_test.go's memSink) only implement Sink
// methods they need. Threading the batch surface through Sink would force
// every fake to grow 13 methods. The shim keeps fakes unchanged and lets
// the real Store provide the perf path via a type assertion.

// commitBatch is a small helper that hides the (n, err) -> prov accounting
// at the end of every connector loop. It accepts any batch handle that
// satisfies the {Commit() (int, error); Rollback()} surface; the typed
// Add methods aren't needed here. Provenance semantics:
//   - n (rows committed across all flushes) is added to prov.RowsReturned[table]
//   - err (first-wins from inside the batch) is recorded only if no prior
//     error is present for that table key.
type committer interface {
	Commit() (int, error)
}

func commitBatch(b committer, prov *connector.Provenance, table string) {
	n, err := b.Commit()
	prov.RowsReturned[table] += n
	if err != nil && prov.Errors[table] == "" {
		prov.Errors[table] = err.Error()
	}
}

// --- per-table opener interfaces (one per hot table) ---
//
// Each interface matches the corresponding *store.Store method exactly so
// the type assertion below picks up the real implementation at runtime.

type commitsBatchOpener interface {
	BeginCommitsBatch() (*store.CommitsBatch, error)
}
type commitFilesBatchOpener interface {
	BeginCommitFilesBatch() (*store.CommitFilesBatch, error)
}
type commitCoauthorsBatchOpener interface {
	BeginCommitCoauthorsBatch() (*store.CommitCoauthorsBatch, error)
}
type prsBatchOpener interface {
	BeginPRsBatch() (*store.PRsBatch, error)
}
type prCommitsBatchOpener interface {
	BeginPRCommitsBatch() (*store.PRCommitsBatch, error)
}
type reviewsBatchOpener interface {
	BeginReviewsBatch() (*store.ReviewsBatch, error)
}
type prCommentsBatchOpener interface {
	BeginPRCommentsBatch() (*store.PRCommentsBatch, error)
}
type prLabelsBatchOpener interface {
	BeginPRLabelsBatch() (*store.PRLabelsBatch, error)
}
type fileMetricsBatchOpener interface {
	BeginFileMetricsBatch() (*store.FileMetricsBatch, error)
}
type fileComplexityHistoryBatchOpener interface {
	BeginFileComplexityHistoryBatch() (*store.FileComplexityHistoryBatch, error)
}
type repoFilesBatchOpener interface {
	BeginRepoFilesBatch() (*store.RepoFilesBatch, error)
}

// --- handle interfaces used by the connector loops ---

type commitsBatch interface {
	Add(model.Commit) error
	Commit() (int, error)
	Rollback()
}
type commitFilesBatch interface {
	Add(model.CommitFile) error
	Commit() (int, error)
	Rollback()
}
type commitCoauthorsBatch interface {
	Add(model.CommitCoauthor) error
	Commit() (int, error)
	Rollback()
}
type prsBatch interface {
	Add(model.PR) error
	Commit() (int, error)
	Rollback()
}
type prCommitsBatch interface {
	Add(model.PRCommit) error
	Commit() (int, error)
	Rollback()
}
type reviewsBatch interface {
	Add(model.Review) error
	Commit() (int, error)
	Rollback()
}
type prCommentsBatch interface {
	Add(model.PRComment) error
	Commit() (int, error)
	Rollback()
}
type prLabelsBatch interface {
	Add(model.PRLabel) error
	Commit() (int, error)
	Rollback()
}
type fileMetricsBatch interface {
	Add(model.FileMetric) error
	Commit() (int, error)
	Rollback()
}
type fileComplexityHistoryBatch interface {
	Add(model.FileComplexityHistory) error
	Commit() (int, error)
	Rollback()
}
type repoFilesBatch interface {
	Add(model.RepoFile) error
	Commit() (int, error)
	Rollback()
}

// --- openers ---
//
// Each openFooBatch checks whether sink implements the corresponding
// opener; if yes, returns the real batch. Otherwise returns a per-row shim.
// On Begin error the shim path is taken so behaviour degrades to the
// existing per-row path rather than aborting.

func openCommitsBatch(sink connector.Sink) commitsBatch {
	if o, ok := sink.(commitsBatchOpener); ok {
		if b, err := o.BeginCommitsBatch(); err == nil {
			return b
		}
	}
	return &commitsShim{sink: sink}
}

func openCommitFilesBatch(sink connector.Sink) commitFilesBatch {
	if o, ok := sink.(commitFilesBatchOpener); ok {
		if b, err := o.BeginCommitFilesBatch(); err == nil {
			return b
		}
	}
	return &commitFilesShim{sink: sink}
}

func openCommitCoauthorsBatch(sink connector.Sink) commitCoauthorsBatch {
	if o, ok := sink.(commitCoauthorsBatchOpener); ok {
		if b, err := o.BeginCommitCoauthorsBatch(); err == nil {
			return b
		}
	}
	return &commitCoauthorsShim{sink: sink}
}

func openPRsBatch(sink connector.Sink) prsBatch {
	if o, ok := sink.(prsBatchOpener); ok {
		if b, err := o.BeginPRsBatch(); err == nil {
			return b
		}
	}
	return &prsShim{sink: sink}
}

func openPRCommitsBatch(sink connector.Sink) prCommitsBatch {
	if o, ok := sink.(prCommitsBatchOpener); ok {
		if b, err := o.BeginPRCommitsBatch(); err == nil {
			return b
		}
	}
	return &prCommitsShim{sink: sink}
}

func openReviewsBatch(sink connector.Sink) reviewsBatch {
	if o, ok := sink.(reviewsBatchOpener); ok {
		if b, err := o.BeginReviewsBatch(); err == nil {
			return b
		}
	}
	return &reviewsShim{sink: sink}
}

func openPRCommentsBatch(sink connector.Sink) prCommentsBatch {
	if o, ok := sink.(prCommentsBatchOpener); ok {
		if b, err := o.BeginPRCommentsBatch(); err == nil {
			return b
		}
	}
	return &prCommentsShim{sink: sink}
}

func openPRLabelsBatch(sink connector.Sink) prLabelsBatch {
	if o, ok := sink.(prLabelsBatchOpener); ok {
		if b, err := o.BeginPRLabelsBatch(); err == nil {
			return b
		}
	}
	return &prLabelsShim{sink: sink}
}

func openFileMetricsBatch(sink connector.Sink) fileMetricsBatch {
	if o, ok := sink.(fileMetricsBatchOpener); ok {
		if b, err := o.BeginFileMetricsBatch(); err == nil {
			return b
		}
	}
	return &fileMetricsShim{sink: sink}
}

func openFileComplexityHistoryBatch(sink connector.Sink) fileComplexityHistoryBatch {
	if o, ok := sink.(fileComplexityHistoryBatchOpener); ok {
		if b, err := o.BeginFileComplexityHistoryBatch(); err == nil {
			return b
		}
	}
	return &fileComplexityHistoryShim{sink: sink}
}

func openRepoFilesBatch(sink connector.Sink) repoFilesBatch {
	if o, ok := sink.(repoFilesBatchOpener); ok {
		if b, err := o.BeginRepoFilesBatch(); err == nil {
			return b
		}
	}
	return &repoFilesShim{sink: sink}
}

// --- shims (per-row fallbacks) ---
//
// Each shim tracks n (successful Adds) and err (first wins). Commit returns
// (n, err) so the connector's loop can record RowsReturned and prov.Errors
// identically regardless of whether it's writing through the batch or the
// shim path. Rollback is a no-op — by the time it's deferred the per-row
// Inserts have already landed; there's nothing to undo.

type commitsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *commitsShim) Add(c model.Commit) error {
	if e := s.sink.InsertCommit(c); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *commitsShim) Commit() (int, error) { return s.n, s.err }
func (s *commitsShim) Rollback()            {}

type commitFilesShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *commitFilesShim) Add(c model.CommitFile) error {
	if e := s.sink.InsertCommitFile(c); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *commitFilesShim) Commit() (int, error) { return s.n, s.err }
func (s *commitFilesShim) Rollback()            {}

type commitCoauthorsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *commitCoauthorsShim) Add(c model.CommitCoauthor) error {
	if e := s.sink.InsertCommitCoauthor(c); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *commitCoauthorsShim) Commit() (int, error) { return s.n, s.err }
func (s *commitCoauthorsShim) Rollback()            {}

type prsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *prsShim) Add(p model.PR) error {
	if e := s.sink.InsertPR(p); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *prsShim) Commit() (int, error) { return s.n, s.err }
func (s *prsShim) Rollback()            {}

type prCommitsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *prCommitsShim) Add(p model.PRCommit) error {
	if e := s.sink.InsertPRCommit(p); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *prCommitsShim) Commit() (int, error) { return s.n, s.err }
func (s *prCommitsShim) Rollback()            {}

type reviewsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *reviewsShim) Add(r model.Review) error {
	if e := s.sink.InsertReview(r); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *reviewsShim) Commit() (int, error) { return s.n, s.err }
func (s *reviewsShim) Rollback()            {}

type prCommentsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *prCommentsShim) Add(c model.PRComment) error {
	if e := s.sink.InsertPRComment(c); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *prCommentsShim) Commit() (int, error) { return s.n, s.err }
func (s *prCommentsShim) Rollback()            {}

type prLabelsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *prLabelsShim) Add(l model.PRLabel) error {
	if e := s.sink.InsertPRLabel(l); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *prLabelsShim) Commit() (int, error) { return s.n, s.err }
func (s *prLabelsShim) Rollback()            {}

type fileMetricsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *fileMetricsShim) Add(f model.FileMetric) error {
	if e := s.sink.InsertFileMetric(f); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *fileMetricsShim) Commit() (int, error) { return s.n, s.err }
func (s *fileMetricsShim) Rollback()            {}

type fileComplexityHistoryShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *fileComplexityHistoryShim) Add(f model.FileComplexityHistory) error {
	if e := s.sink.InsertFileComplexityHistory(f); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *fileComplexityHistoryShim) Commit() (int, error) { return s.n, s.err }
func (s *fileComplexityHistoryShim) Rollback()            {}

type repoFilesShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *repoFilesShim) Add(f model.RepoFile) error {
	if e := s.sink.InsertRepoFile(f); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *repoFilesShim) Commit() (int, error) { return s.n, s.err }
func (s *repoFilesShim) Rollback()            {}
