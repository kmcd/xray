package bugsnag

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

// stubSink records inserts and fails the Nth InsertIncident.
type stubSink struct {
	incidents      []model.Incident
	failOnIncident int
	incidentCalls  int
}

func (s *stubSink) InsertIncident(inc model.Incident) error {
	s.incidentCalls++
	if s.failOnIncident != 0 && s.incidentCalls == s.failOnIncident {
		return errors.New("simulated incident insert failure")
	}
	s.incidents = append(s.incidents, inc)
	return nil
}

// All other Sink methods are no-ops.
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
func (s *stubSink) InsertDeploy(model.Deploy) error                              { return nil }
func (s *stubSink) InsertRelease(model.Release) error                            { return nil }
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
		baseURL:    baseURL,
	}
}

// TestExtract_Forbidden_RecordsProjectInaccessible covers bugsnag/extract.go
// — a 403 on the project errors list must set
// Endpoints["project:<id>"]={Accessible:false}.
func TestExtract_Forbidden_RecordsProjectInaccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/p1/errors", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	c.projects = map[string]string{"p1": "kmcd/foo"}
	sink := &stubSink{}
	prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, sink)

	ep, ok := prov.Endpoints["project:p1"]
	if !ok {
		t.Fatalf("expected endpoints[project:p1] entry on 403")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on 403; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on 403; got empty")
	}
}

// TestExtract_Success_RecordsProjectAccessible covers the success path.
func TestExtract_Success_RecordsProjectAccessible(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/p1/errors", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	c.projects = map[string]string{"p1": "kmcd/foo"}
	sink := &stubSink{}
	prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	}, sink)

	if ep := prov.Endpoints["project:p1"]; !ep.Accessible {
		t.Errorf("expected Accessible=true on success; got %+v", ep)
	}
}

// TestListErrors_InsertError_ContinuesWalk_PerIDKey verifies that a failing
// InsertIncident on the second of three errors doesn't truncate the page or
// the outer next-link walk. Per-id key recorded in prov.Errors; complete=true
// because pagination wasn't interrupted (the single failure didn't abort it).
func TestListErrors_InsertError_ContinuesWalk_PerIDKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/p1/errors", func(w http.ResponseWriter, _ *http.Request) {
		t0 := time.Date(2026, 6, 9, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
		fmt.Fprintf(w, `[
			{"id": "e-1", "first_seen": %q, "last_seen": %q, "status": "open",  "severity": "error", "events": 5},
			{"id": "e-2", "first_seen": %q, "last_seen": %q, "status": "open",  "severity": "error", "events": 6},
			{"id": "e-3", "first_seen": %q, "last_seen": %q, "status": "open",  "severity": "error", "events": 7}
		]`, t0, t0, t0, t0, t0, t0)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testConnector(t, srv.URL)
	sink := &stubSink{failOnIncident: 2}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", connector.Window{
		Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
	})

	rows, complete, err := c.listErrors(context.Background(), "p1", "kmcd/foo", connector.Window{
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
	if sink.incidentCalls != 3 {
		t.Errorf("expected 3 InsertIncident attempts (walk continues past failure); got %d", sink.incidentCalls)
	}
	var found bool
	for k := range prov.Errors {
		if strings.HasPrefix(k, "incidents:") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected per-id prov.Errors[incidents:<id>] entry; got %v", prov.Errors)
	}
}

// TestCappedWindow verifies that cappedWindow trims a window longer than
// maxWindowDays and leaves a shorter window unchanged.
func TestCappedWindow(t *testing.T) {
	c := &Connector{maxWindowDays: 60}

	end := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)

	t.Run("long window is capped", func(t *testing.T) {
		w := connector.Window{
			Start: end.Add(-365 * 24 * time.Hour),
			End:   end,
		}
		got := c.cappedWindow(w)
		want := end.Add(-60 * 24 * time.Hour)
		if !got.Start.Equal(want) {
			t.Errorf("Start: want %v, got %v", want, got.Start)
		}
		if !got.End.Equal(end) {
			t.Errorf("End: want %v, got %v", end, got.End)
		}
	})

	t.Run("short window is unchanged", func(t *testing.T) {
		w := connector.Window{
			Start: end.Add(-7 * 24 * time.Hour),
			End:   end,
		}
		got := c.cappedWindow(w)
		if !got.Start.Equal(w.Start) {
			t.Errorf("Start: want %v, got %v", w.Start, got.Start)
		}
	})
}

// TestExtract_MaxWindowDays_ConfigDepth verifies that max_window_days is
// recorded in ConfigDepth when the cap actually narrows the global window,
// and absent when the window fits within the cap.
func TestExtract_MaxWindowDays_ConfigDepth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/p1/errors", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	end := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	c := testConnector(t, srv.URL)
	c.maxWindowDays = 60
	c.projects = map[string]string{"p1": "kmcd/foo"}
	sink := &stubSink{}

	t.Run("cap narrows window: ConfigDepth written", func(t *testing.T) {
		prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
			Start: end.Add(-365 * 24 * time.Hour), // 1y window exceeds 60d cap
			End:   end,
		}, sink)
		// Value is the effective date range, not the day count, so the analyser
		// has unambiguous coverage boundaries without recomputation.
		wantStart := end.Add(-60 * 24 * time.Hour).Format("2006-01-02")
		wantEnd := end.Format("2006-01-02")
		want := wantStart + ".." + wantEnd
		if got := prov.ConfigDepth["max_window_days"]; got != want {
			t.Errorf("ConfigDepth[max_window_days] = %q, want %q", got, want)
		}
	})

	t.Run("window within cap: ConfigDepth absent", func(t *testing.T) {
		prov := c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
			Start: end.Add(-7 * 24 * time.Hour), // 7d fits within 60d cap
			End:   end,
		}, sink)
		if v, ok := prov.ConfigDepth["max_window_days"]; ok {
			t.Errorf("ConfigDepth[max_window_days] = %q, want absent (window fits cap)", v)
		}
	})
}

// TestExtract_WindowIsCapped verifies that listErrors is called with a
// capped window start when the global window exceeds maxWindowDays.
func TestExtract_WindowIsCapped(t *testing.T) {
	var gotSince string
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/p1/errors", func(w http.ResponseWriter, r *http.Request) {
		gotSince = r.URL.Query().Get("filters[event.since]")
		fmt.Fprintln(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	end := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	c := testConnector(t, srv.URL)
	c.maxWindowDays = 60
	c.projects = map[string]string{"p1": "kmcd/foo"}
	sink := &stubSink{}

	c.Extract(context.Background(), connector.Repo{Slug: "kmcd/foo"}, connector.Window{
		Start: end.Add(-365 * 24 * time.Hour),
		End:   end,
	}, sink)

	wantSince := end.Add(-60 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if gotSince != wantSince {
		t.Errorf("filters[event.since]: want %s, got %s", wantSince, gotSince)
	}
}
