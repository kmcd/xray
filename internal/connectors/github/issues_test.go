package github

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// issueJSON is the minimal REST issue shape the tests serve. Fields left
// unset serialise to their zero values, which go-github decodes as absent.
type issueJSON struct {
	Number      int             `json:"number"`
	CreatedAt   string          `json:"created_at"`
	ClosedAt    *string         `json:"closed_at,omitempty"`
	State       string          `json:"state"`
	User        *userJSON       `json:"user,omitempty"`
	Labels      []labelJSON     `json:"labels,omitempty"`
	Milestone   *milestoneJSON  `json:"milestone,omitempty"`
	PullRequest *pullRequestRef `json:"pull_request,omitempty"`
}

type userJSON struct {
	Login string `json:"login"`
}
type labelJSON struct {
	Name string `json:"name"`
}
type milestoneJSON struct {
	Title string `json:"title"`
}
type pullRequestRef struct {
	URL string `json:"url"`
}

// issuesServer returns an httptest server that serves the supplied issues on
// the first page of GET /repos/o/r/issues and an empty array on any further
// page. Non-issues paths 404.
func issuesServer(t *testing.T, issues []issueJSON) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/issues" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if p := r.URL.Query().Get("page"); p != "" && p != "1" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
}

func issuesTestWindow() connector.Window {
	return connector.Window{
		Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
	}
}

func issueTS(month, day int) string {
	return time.Date(2024, time.Month(month), day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func issuesConnector(t *testing.T, srv *httptest.Server, cfg config.GitHubConn) *Connector {
	t.Helper()
	cfg.Token = "test-token"
	c, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.setBaseURL(srv.URL); err != nil {
		t.Fatalf("setBaseURL: %v", err)
	}
	return c
}

func runExtractIssues(t *testing.T, c *Connector, sink *memSink) connector.Provenance {
	t.Helper()
	prov := connector.NewProvenance("github", "o/r", issuesTestWindow())
	c.extractIssues(context.Background(), connector.Repo{Slug: "o/r"}, issuesTestWindow(), sink, &prov)
	return prov
}

func incidentByID(incs []model.Incident, id string) *model.Incident {
	for i := range incs {
		if incs[i].ID == id {
			return &incs[i]
		}
	}
	return nil
}

func strptr(s string) *string { return &s }

func TestExtractIssues_BugAndRegressionAndBoth(t *testing.T) {
	srv := issuesServer(t, []issueJSON{
		{Number: 1, CreatedAt: issueTS(2, 1), State: "open", User: &userJSON{Login: "alice"}, Labels: []labelJSON{{Name: "bug"}}},
		{Number: 2, CreatedAt: issueTS(2, 2), ClosedAt: strptr(issueTS(3, 1)), State: "closed", User: &userJSON{Login: "bob"}, Labels: []labelJSON{{Name: "regression"}}, Milestone: &milestoneJSON{Title: "v2.4.0"}},
		{Number: 3, CreatedAt: issueTS(2, 3), State: "open", User: &userJSON{Login: "carol"}, Labels: []labelJSON{{Name: "bug"}, {Name: "regression"}}},
	})
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{})
	sink := &memSink{}
	prov := runExtractIssues(t, c, sink)

	// #1 → defect only; #2 → incident only; #3 → both.
	if len(sink.defects) != 2 {
		t.Fatalf("defects = %d, want 2: %+v", len(sink.defects), sink.defects)
	}
	if len(sink.incidents) != 2 {
		t.Fatalf("incidents = %d, want 2: %+v", len(sink.incidents), sink.incidents)
	}
	if prov.RowsReturned["defects"] != 2 || prov.RowsReturned["incidents"] != 2 {
		t.Fatalf("rows = %+v", prov.RowsReturned)
	}
	if !prov.Endpoints["issues"].Accessible {
		t.Fatalf("issues endpoint not accessible: %+v", prov.Endpoints["issues"])
	}

	d := sink.defects[0]
	if d.Source != "github_issues" || d.TicketRef != "1" || d.ClosedAt != nil {
		t.Fatalf("defect[0] unexpected: %+v", d)
	}
	if d.ID != "o/r:github_issues:1:1" {
		t.Fatalf("defect[0] id = %q", d.ID)
	}

	inc2 := incidentByID(sink.incidents, "2")
	if inc2 == nil {
		t.Fatalf("no incident with id 2: %+v", sink.incidents)
	}
	if !inc2.IsRegression || inc2.Source != "github_issues" {
		t.Fatalf("incident #2 wrong: %+v", *inc2)
	}
	if inc2.ResolvedAt == nil {
		t.Fatalf("incident #2 should be resolved")
	}
	if inc2.ReleaseRef != "v2.4.0" {
		t.Fatalf("incident #2 release_ref = %q, want v2.4.0", inc2.ReleaseRef)
	}
}

func TestExtractIssues_SkipsPRsAndBots(t *testing.T) {
	srv := issuesServer(t, []issueJSON{
		{Number: 10, CreatedAt: issueTS(2, 1), State: "open", User: &userJSON{Login: "alice"}, Labels: []labelJSON{{Name: "bug"}}, PullRequest: &pullRequestRef{URL: "https://api.github.com/repos/o/r/pulls/10"}},
		{Number: 11, CreatedAt: issueTS(2, 2), State: "open", User: &userJSON{Login: "dependabot[bot]"}, Labels: []labelJSON{{Name: "bug"}}},
		{Number: 12, CreatedAt: issueTS(2, 3), State: "open", User: &userJSON{Login: "alice"}, Labels: []labelJSON{{Name: "documentation"}}},
	})
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{})
	sink := &memSink{}
	prov := runExtractIssues(t, c, sink)

	// PR-as-issue skipped, bot skipped, non-bug/non-regression skipped:
	// zero rows, but the endpoint is reachable.
	if len(sink.defects) != 0 || len(sink.incidents) != 0 {
		t.Fatalf("expected no rows, got defects=%d incidents=%d", len(sink.defects), len(sink.incidents))
	}
	if !prov.Endpoints["issues"].Accessible {
		t.Fatalf("issues endpoint should be accessible")
	}
}

func TestExtractIssues_SeverityMapCaseInsensitive(t *testing.T) {
	srv := issuesServer(t, []issueJSON{
		{Number: 5, CreatedAt: issueTS(4, 1), State: "open", User: &userJSON{Login: "alice"}, Labels: []labelJSON{{Name: "Regression"}, {Name: "Sev1"}}},
	})
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{IssueSeverityLabels: map[string]string{"sev1": "critical"}})
	sink := &memSink{}
	runExtractIssues(t, c, sink)

	if len(sink.incidents) != 1 {
		t.Fatalf("incidents = %d, want 1", len(sink.incidents))
	}
	if sink.incidents[0].Severity != "critical" {
		t.Fatalf("severity = %q, want critical", sink.incidents[0].Severity)
	}
}

func TestExtractIssues_CustomBugLabels(t *testing.T) {
	srv := issuesServer(t, []issueJSON{
		{Number: 7, CreatedAt: issueTS(4, 1), State: "open", User: &userJSON{Login: "a"}, Labels: []labelJSON{{Name: "bug"}}},       // default-only, should NOT match
		{Number: 8, CreatedAt: issueTS(4, 2), State: "open", User: &userJSON{Login: "a"}, Labels: []labelJSON{{Name: "Type:Bug"}}}, // custom, case-insensitive
	})
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{IssueBugLabels: []string{"type:bug"}})
	sink := &memSink{}
	runExtractIssues(t, c, sink)

	if len(sink.defects) != 1 {
		t.Fatalf("defects = %d, want 1 (only the custom-label issue): %+v", len(sink.defects), sink.defects)
	}
	if sink.defects[0].TicketRef != "8" {
		t.Fatalf("matched wrong issue: %+v", sink.defects[0])
	}
}

func TestExtractIssues_WindowGating(t *testing.T) {
	srv := issuesServer(t, []issueJSON{
		{Number: 1, CreatedAt: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), State: "open", User: &userJSON{Login: "a"}, Labels: []labelJSON{{Name: "bug"}}}, // before window
		{Number: 2, CreatedAt: issueTS(6, 1), State: "open", User: &userJSON{Login: "a"}, Labels: []labelJSON{{Name: "bug"}}},                                                     // in window
		{Number: 3, CreatedAt: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339), State: "open", User: &userJSON{Login: "a"}, Labels: []labelJSON{{Name: "bug"}}}, // after window → stops paging
	})
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{})
	sink := &memSink{}
	runExtractIssues(t, c, sink)

	if len(sink.defects) != 1 || sink.defects[0].TicketRef != "2" {
		t.Fatalf("window gating wrong: %+v", sink.defects)
	}
}

func TestExtractIssues_EndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{})
	sink := &memSink{}
	prov := runExtractIssues(t, c, sink)

	if prov.Endpoints["issues"].Accessible {
		t.Fatalf("endpoint should be inaccessible on 403")
	}
	if prov.PaginationComplete {
		t.Fatalf("pagination should be marked incomplete on error")
	}
	if prov.Errors["issues"] == "" {
		t.Fatalf("expected an error recorded for issues")
	}
	if len(sink.defects) != 0 || len(sink.incidents) != 0 {
		t.Fatalf("no rows expected on error")
	}
}

func TestExtractIssues_CancellationIsTruncationNotInaccessible(t *testing.T) {
	srv := issuesServer(t, []issueJSON{
		{Number: 1, CreatedAt: issueTS(2, 1), State: "open", User: &userJSON{Login: "a"}, Labels: []labelJSON{{Name: "bug"}}},
	})
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{})
	sink := &memSink{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the walk starts

	prov := connector.NewProvenance("github", "o/r", issuesTestWindow())
	c.extractIssues(ctx, connector.Repo{Slug: "o/r"}, issuesTestWindow(), sink, &prov)

	// A cancelled context is truncation, not a permission denial: the
	// endpoint must stay accessible so the analyser does not read it as
	// "no signal", but pagination is incomplete.
	if !prov.Endpoints["issues"].Accessible {
		t.Fatalf("cancelled walk should keep endpoint accessible (truncation), got %+v", prov.Endpoints["issues"])
	}
	if prov.PaginationComplete {
		t.Fatalf("cancelled walk should mark pagination incomplete")
	}
}

func TestExtractIssues_InvalidSlug(t *testing.T) {
	srv := issuesServer(t, nil)
	defer srv.Close()
	c := issuesConnector(t, srv, config.GitHubConn{})
	sink := &memSink{}
	prov := connector.NewProvenance("github", "no-slash", issuesTestWindow())
	c.extractIssues(context.Background(), connector.Repo{Slug: "no-slash"}, issuesTestWindow(), sink, &prov)
	if prov.Endpoints["issues"].Accessible {
		t.Fatalf("invalid slug should be inaccessible")
	}
}
