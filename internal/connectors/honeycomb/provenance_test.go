package honeycomb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// stubSink records inserts and fails the Nth InsertDeploy.
type stubSink struct {
	deploys      []model.Deploy
	failOnDeploy int
	deployCalls  int
}

func (s *stubSink) InsertDeploy(d model.Deploy) error {
	s.deployCalls++
	if s.failOnDeploy != 0 && s.deployCalls == s.failOnDeploy {
		return errors.New("simulated deploy insert failure")
	}
	s.deploys = append(s.deploys, d)
	return nil
}

// All other Sink methods are no-ops for these tests.
func (s *stubSink) InsertRepo(model.Repo) error                                  { return nil }
func (s *stubSink) InsertTeamRepo(string, string) error                          { return nil }
func (s *stubSink) InsertRepoLanguage(model.RepoLanguage) error                  { return nil }
func (s *stubSink) InsertBranch(model.Branch) error                              { return nil }
func (s *stubSink) InsertBranchProtection(model.BranchProtection) error          { return nil }
func (s *stubSink) InsertCodeowner(model.Codeowner) error                        { return nil }
func (s *stubSink) InsertCommit(model.Commit) error                              { return nil }
func (s *stubSink) InsertCommitFile(model.CommitFile) error                      { return nil }
func (s *stubSink) InsertCommitCoauthor(model.CommitCoauthor) error              { return nil }
func (s *stubSink) InsertPR(model.PR) error                                      { return nil }
func (s *stubSink) InsertPRCommit(model.PRCommit) error                          { return nil }
func (s *stubSink) InsertReview(model.Review) error                              { return nil }
func (s *stubSink) InsertPRComment(model.PRComment) error                        { return nil }
func (s *stubSink) InsertPRReviewRequest(model.PRReviewRequest) error            { return nil }
func (s *stubSink) InsertPRLabel(model.PRLabel) error                            { return nil }
func (s *stubSink) InsertBuild(model.Build) error                                { return nil }
func (s *stubSink) InsertBuildJob(model.BuildJob) error                          { return nil }
func (s *stubSink) InsertRelease(model.Release) error                            { return nil }
func (s *stubSink) InsertIncident(model.Incident) error                          { return nil }
func (s *stubSink) InsertDefect(model.Defect) error                              { return nil }
func (s *stubSink) InsertFileMetric(model.FileMetric) error                      { return nil }
func (s *stubSink) InsertHarnessArtifact(model.HarnessArtifact) error            { return nil }
func (s *stubSink) InsertFileComplexityHistory(model.FileComplexityHistory) error { return nil }
func (s *stubSink) InsertRepoFile(model.RepoFile) error                          { return nil }

func testConnector(t *testing.T, baseURL string) *Connector {
	t.Helper()
	return &Connector{
		httpClient: &http.Client{},
		log:        slog.Default(),
		token:      "test-token",
		dataset:    "myds",
		baseURL:    baseURL,
	}
}

// TestExtract_Forbidden_RecordsMarkersInaccessible covers honeycomb/extract.go
// — a 403 on the markers list call must set Endpoints["markers"]={Accessible:false}.
func TestExtract_Forbidden_RecordsMarkersInaccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/markers/myds", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	sink := &stubSink{}
	prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, sink)

	ep, ok := prov.Endpoints["markers"]
	if !ok {
		t.Fatalf("expected endpoints[markers] entry on 403")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on 403; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on 403; got empty")
	}
}

// TestExtract_Success_RecordsMarkersAccessible covers the success path.
func TestExtract_Success_RecordsMarkersAccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/markers/myds", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	sink := &stubSink{}
	prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, sink)

	if ep := prov.Endpoints["markers"]; !ep.Accessible {
		t.Errorf("expected Accessible=true on success; got %+v", ep)
	}
}

// TestExtractDeploys_InsertError_ContinuesWalk_PerIDKey verifies that a failing
// InsertDeploy on the second of three markers doesn't truncate the walk and
// records a per-id key in prov.Errors. The function still returns complete=true
// because every marker was processed (just one row failed); pagination wasn't
// interrupted.
func TestExtractDeploys_InsertError_ContinuesWalk_PerIDKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/markers/myds", func(w http.ResponseWriter, _ *http.Request) {
		t0 := time.Date(2026, 6, 9, 1, 0, 0, 0, time.UTC).Unix()
		fmt.Fprintf(w, `[
			{"id": "m-1", "message": "v1.0.0", "type": "production", "start_time": %d},
			{"id": "m-2", "message": "v1.1.0", "type": "production", "start_time": %d},
			{"id": "m-3", "message": "v1.2.0", "type": "production", "start_time": %d}
		]`, t0, t0+60, t0+120)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	sink := &stubSink{failOnDeploy: 2}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	})

	rows, complete, err := c.extractDeploys(context.Background(), "kmcd/foo", connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, sink, &prov)
	if err != nil {
		t.Errorf("expected nil err after per-row continue; got %v", err)
	}
	if !complete {
		t.Errorf("expected complete=true after walk drains; got false")
	}
	if rows != 2 {
		t.Errorf("expected rows=2 (successful inserts); got %d", rows)
	}
	if sink.deployCalls != 3 {
		t.Errorf("expected 3 InsertDeploy attempts (walk continues past failure); got %d", sink.deployCalls)
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
