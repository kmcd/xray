package circleci

import (
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/store"
)

// Shim plumbing — see internal/connectors/github/batch.go for the rationale.
// circleci writes builds and build_jobs only.

type buildsBatchOpener interface {
	BeginBuildsBatch() (*store.BuildsBatch, error)
}
type buildJobsBatchOpener interface {
	BeginBuildJobsBatch() (*store.BuildJobsBatch, error)
}

type buildsBatch interface {
	Add(model.Build) error
	Commit() (int, error)
	Rollback()
}
type buildJobsBatch interface {
	Add(model.BuildJob) error
	Commit() (int, error)
	Rollback()
}

func openBuildsBatch(sink connector.Sink) buildsBatch {
	if o, ok := sink.(buildsBatchOpener); ok {
		if b, err := o.BeginBuildsBatch(); err == nil {
			return b
		}
	}
	return &buildsShim{sink: sink}
}

func openBuildJobsBatch(sink connector.Sink) buildJobsBatch {
	if o, ok := sink.(buildJobsBatchOpener); ok {
		if b, err := o.BeginBuildJobsBatch(); err == nil {
			return b
		}
	}
	return &buildJobsShim{sink: sink}
}

type buildsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *buildsShim) Add(b model.Build) error {
	if e := s.sink.InsertBuild(b); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *buildsShim) Commit() (int, error) { return s.n, s.err }
func (s *buildsShim) Rollback()            {}

type buildJobsShim struct {
	sink connector.Sink
	n    int
	err  error
}

func (s *buildJobsShim) Add(j model.BuildJob) error {
	if e := s.sink.InsertBuildJob(j); e != nil {
		if s.err == nil {
			s.err = e
		}
		return e
	}
	s.n++
	return nil
}
func (s *buildJobsShim) Commit() (int, error) { return s.n, s.err }
func (s *buildJobsShim) Rollback()            {}

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
