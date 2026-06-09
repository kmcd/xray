package githubactions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// failingSink wraps memSink and fails the Nth call to specific insert methods.
type failingSink struct {
	memSink
	failOnBuild       int
	buildCalls        int
	failOnBuildJob    int
	buildJobCalls     int
	failOnDeploy      int
	deployCalls       int
}

func (s *failingSink) InsertBuild(b model.Build) error {
	s.buildCalls++
	if s.failOnBuild != 0 && s.buildCalls == s.failOnBuild {
		return errors.New("simulated build insert failure")
	}
	return s.memSink.InsertBuild(b)
}

func (s *failingSink) InsertBuildJob(j model.BuildJob) error {
	s.buildJobCalls++
	if s.failOnBuildJob != 0 && s.buildJobCalls == s.failOnBuildJob {
		return errors.New("simulated build_job insert failure")
	}
	return s.memSink.InsertBuildJob(j)
}

func (s *failingSink) InsertDeploy(d model.Deploy) error {
	s.deployCalls++
	if s.failOnDeploy != 0 && s.deployCalls == s.failOnDeploy {
		return errors.New("simulated deploy insert failure")
	}
	return s.memSink.InsertDeploy(d)
}

// newTestConnector wires a Connector against the supplied httptest.Server URL.
func newTestConnector(t *testing.T, srv *httptest.Server) *Connector {
	t.Helper()
	c := newForTest(srv.Client())
	u, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	c.client.BaseURL = u
	c.client.UploadURL = u
	return c
}

// TestBuilds_InsertError_ContinuesWalk_PerIDKey verifies that a failing
// InsertBuild on the second of three runs doesn't abort the page: the third
// run still emits, and prov.Errors carries a per-id key for the failure.
func TestBuilds_InsertError_ContinuesWalk_PerIDKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/actions/runs", func(w http.ResponseWriter, _ *http.Request) {
		// Three runs all in window.
		fmt.Fprintln(w, `{
			"total_count": 3,
			"workflow_runs": [
				{"id": 1001, "name": "CI",  "status": "completed", "conclusion": "success", "head_sha": "aaa", "head_branch": "main", "event": "push", "created_at": "2026-06-09T01:00:00Z", "run_started_at": "2026-06-09T01:00:00Z", "updated_at": "2026-06-09T01:05:00Z", "run_attempt": 1},
				{"id": 1002, "name": "CI",  "status": "completed", "conclusion": "success", "head_sha": "bbb", "head_branch": "main", "event": "push", "created_at": "2026-06-09T01:01:00Z", "run_started_at": "2026-06-09T01:01:00Z", "updated_at": "2026-06-09T01:06:00Z", "run_attempt": 1},
				{"id": 1003, "name": "CI",  "status": "completed", "conclusion": "success", "head_sha": "ccc", "head_branch": "main", "event": "push", "created_at": "2026-06-09T01:02:00Z", "run_started_at": "2026-06-09T01:02:00Z", "updated_at": "2026-06-09T01:07:00Z", "run_attempt": 1}
			]
		}`)
	})
	mux.HandleFunc("/repos/kmcd/foo/actions/runs/", func(w http.ResponseWriter, _ *http.Request) {
		// jobs endpoint for each run — return empty so the test only exercises Build inserts.
		fmt.Fprintln(w, `{"total_count": 0, "jobs": []}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &failingSink{failOnBuild: 2}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", testWindow)

	c.builds(context.Background(), "kmcd", "foo", connector.Repo{Slug: "kmcd/foo"}, testWindow, sink, &prov)

	if sink.buildCalls != 3 {
		t.Errorf("expected 3 InsertBuild attempts (walk continues past failure); got %d", sink.buildCalls)
	}
	if prov.RowsReturned["builds"] != 2 {
		t.Errorf("RowsReturned[builds]=%d should equal successful inserts (2)", prov.RowsReturned["builds"])
	}
	var found bool
	for k := range prov.Errors {
		if strings.HasPrefix(k, "builds:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected per-id prov.Errors[builds:<id>] entry; got %v", prov.Errors)
	}
}

// TestBuilds_InsertDeployError_ContinuesWalk_PerIDKey covers deploys.go:62.
func TestDeploys_InsertError_ContinuesWalk_PerIDKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/deployments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `[
			{"id": 2001, "sha": "aaa", "environment": "prod", "task": "deploy", "ref": "v1", "created_at": "2026-06-09T01:00:00Z"},
			{"id": 2002, "sha": "bbb", "environment": "prod", "task": "deploy", "ref": "v2", "created_at": "2026-06-09T01:01:00Z"},
			{"id": 2003, "sha": "ccc", "environment": "prod", "task": "deploy", "ref": "v3", "created_at": "2026-06-09T01:02:00Z"}
		]`)
	})
	mux.HandleFunc("/repos/kmcd/foo/deployments/", func(w http.ResponseWriter, _ *http.Request) {
		// statuses endpoint for each deployment — return empty.
		fmt.Fprintln(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &failingSink{failOnDeploy: 2}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", testWindow)

	c.deploys(context.Background(), "kmcd", "foo", connector.Repo{Slug: "kmcd/foo"}, testWindow, sink, &prov)

	if sink.deployCalls != 3 {
		t.Errorf("expected 3 InsertDeploy attempts (walk continues past failure); got %d", sink.deployCalls)
	}
	if prov.RowsReturned["deploys"] != 2 {
		t.Errorf("RowsReturned[deploys]=%d should equal successful inserts (2)", prov.RowsReturned["deploys"])
	}
	var found bool
	for k := range prov.Errors {
		if strings.HasPrefix(k, "deploys:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected per-id prov.Errors[deploys:<id>] entry; got %v", prov.Errors)
	}
}

// TestJobsForRun_InsertError_ContinuesWalk_PerIDKey covers builds.go:114.
func TestJobsForRun_InsertError_ContinuesWalk_PerIDKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/actions/runs/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{
			"total_count": 3,
			"jobs": [
				{"id": 9001, "name": "lint",  "status": "completed", "conclusion": "success", "started_at": "2026-06-09T01:00:00Z", "completed_at": "2026-06-09T01:01:00Z"},
				{"id": 9002, "name": "test",  "status": "completed", "conclusion": "success", "started_at": "2026-06-09T01:01:00Z", "completed_at": "2026-06-09T01:05:00Z"},
				{"id": 9003, "name": "build", "status": "completed", "conclusion": "success", "started_at": "2026-06-09T01:05:00Z", "completed_at": "2026-06-09T01:07:00Z"}
			]
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &failingSink{failOnBuildJob: 2}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", testWindow)

	runID := int64(7777)
	attempt := 1
	run := &github.WorkflowRun{ID: &runID, RunAttempt: &attempt}
	c.jobsForRun(context.Background(), "kmcd", "foo", "kmcd/foo", run, sink, &prov)

	if sink.buildJobCalls != 3 {
		t.Errorf("expected 3 InsertBuildJob attempts (walk continues past failure); got %d", sink.buildJobCalls)
	}
	if prov.RowsReturned["build_jobs"] != 2 {
		t.Errorf("RowsReturned[build_jobs]=%d should equal successful inserts (2)", prov.RowsReturned["build_jobs"])
	}
	var found bool
	for k := range prov.Errors {
		if strings.HasPrefix(k, "build_jobs:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected per-id prov.Errors[build_jobs:<runID>:<name>] entry; got %v", prov.Errors)
	}
}
