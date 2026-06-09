package sentry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// stubSink is a no-op Sink implementation.
type stubSink struct{}

func (stubSink) InsertRepo(model.Repo) error                                  { return nil }
func (stubSink) InsertTeamRepo(string, string) error                          { return nil }
func (stubSink) InsertRepoLanguage(model.RepoLanguage) error                  { return nil }
func (stubSink) InsertBranch(model.Branch) error                              { return nil }
func (stubSink) InsertBranchProtection(model.BranchProtection) error          { return nil }
func (stubSink) InsertCodeowner(model.Codeowner) error                        { return nil }
func (stubSink) InsertCommit(model.Commit) error                              { return nil }
func (stubSink) InsertCommitFile(model.CommitFile) error                      { return nil }
func (stubSink) InsertCommitCoauthor(model.CommitCoauthor) error              { return nil }
func (stubSink) InsertPR(model.PR) error                                      { return nil }
func (stubSink) InsertPRCommit(model.PRCommit) error                          { return nil }
func (stubSink) InsertReview(model.Review) error                              { return nil }
func (stubSink) InsertPRComment(model.PRComment) error                        { return nil }
func (stubSink) InsertPRReviewRequest(model.PRReviewRequest) error            { return nil }
func (stubSink) InsertPRLabel(model.PRLabel) error                            { return nil }
func (stubSink) InsertBuild(model.Build) error                                { return nil }
func (stubSink) InsertBuildJob(model.BuildJob) error                          { return nil }
func (stubSink) InsertDeploy(model.Deploy) error                              { return nil }
func (stubSink) InsertRelease(model.Release) error                            { return nil }
func (stubSink) InsertIncident(model.Incident) error                          { return nil }
func (stubSink) InsertDefect(model.Defect) error                              { return nil }
func (stubSink) InsertFileMetric(model.FileMetric) error                      { return nil }
func (stubSink) InsertHarnessArtifact(model.HarnessArtifact) error            { return nil }
func (stubSink) InsertFileComplexityHistory(model.FileComplexityHistory) error { return nil }
func (stubSink) InsertRepoFile(model.RepoFile) error                          { return nil }

func testConnector(t *testing.T, baseURL string) *Connector {
	t.Helper()
	return &Connector{
		httpClient: &http.Client{},
		log:        slog.Default(),
		token:      "test-token",
		org:        "myorg",
		baseURL:    baseURL,
	}
}

// TestExtract_Forbidden_RecordsSentrySlugInaccessible covers sentry/extract.go
// — a 403 on the issues list must set
// Endpoints[<sentry-slug>]={Accessible:false}.
func TestExtract_Forbidden_RecordsSentrySlugInaccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/myorg/proj1/issues/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	c.projects = map[string]string{"proj1": "kmcd/foo"}
	prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, stubSink{})

	ep, ok := prov.Endpoints["proj1"]
	if !ok {
		t.Fatalf("expected endpoints[proj1] entry on 403")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on 403; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on 403; got empty")
	}
}

// TestExtract_Success_RecordsSentrySlugAccessible covers the success path.
func TestExtract_Success_RecordsSentrySlugAccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/myorg/proj1/issues/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	c.projects = map[string]string{"proj1": "kmcd/foo"}
	prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, stubSink{})

	if ep := prov.Endpoints["proj1"]; !ep.Accessible {
		t.Errorf("expected Accessible=true on success; got %+v", ep)
	}
}
