package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// setupCloneWithRemoteRefs creates a tmp git repo with one commit and
// fakes refs/remotes/origin/<branch> for every name in branches. Returns
// the repo path. Used by tests that exercise the local-clone read paths
// added in #72 (branches list, codeowners, languages — codeowners and
// languages don't actually need the refs, just the dir).
func setupCloneWithRemoteRefs(t *testing.T, branches []string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		// #nosec G204 -- args are test-controlled literals.
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	run("config", "commit.gpgsign", "false")
	// Empty commit so HEAD has a SHA to point refs at.
	run("commit", "--allow-empty", "-q", "-m", "seed")
	headOut, err := exec.CommandContext(t.Context(), "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(headOut))
	for _, b := range branches {
		run("update-ref", "refs/remotes/origin/"+b, sha)
	}
	return dir
}

// memSink is an in-memory implementation of connector.Sink for HTTP-path
// tests. It records every row inserted so test bodies can assert against
// them. Methods are intentionally trivial — no validation, no FK checking.
type memSink struct {
	prs              []model.PR
	prCommits        []model.PRCommit
	prLabels         []model.PRLabel
	branches         []model.Branch
	branchProtection []model.BranchProtection
}

func (s *memSink) InsertRepo(model.Repo) error                       { return nil }
func (s *memSink) InsertTeamRepo(string, string) error               { return nil }
func (s *memSink) InsertRepoLanguage(model.RepoLanguage) error       { return nil }
func (s *memSink) InsertBranch(b model.Branch) error                 { s.branches = append(s.branches, b); return nil }
func (s *memSink) InsertBranchProtection(b model.BranchProtection) error {
	s.branchProtection = append(s.branchProtection, b)
	return nil
}
func (s *memSink) InsertCodeowner(model.Codeowner) error             { return nil }
func (s *memSink) InsertCommit(model.Commit) error                   { return nil }
func (s *memSink) InsertCommitFile(model.CommitFile) error           { return nil }
func (s *memSink) InsertCommitCoauthor(model.CommitCoauthor) error   { return nil }
func (s *memSink) InsertPR(p model.PR) error                         { s.prs = append(s.prs, p); return nil }
func (s *memSink) InsertPRCommit(p model.PRCommit) error {
	s.prCommits = append(s.prCommits, p)
	return nil
}
func (s *memSink) InsertReview(model.Review) error                   { return nil }
func (s *memSink) InsertPRComment(model.PRComment) error             { return nil }
func (s *memSink) InsertPRReviewRequest(model.PRReviewRequest) error { return nil }
func (s *memSink) InsertPRLabel(p model.PRLabel) error               { s.prLabels = append(s.prLabels, p); return nil }
func (s *memSink) InsertBuild(model.Build) error                     { return nil }
func (s *memSink) InsertBuildJob(model.BuildJob) error               { return nil }
func (s *memSink) InsertDeploy(model.Deploy) error                   { return nil }
func (s *memSink) InsertRelease(model.Release) error                 { return nil }
func (s *memSink) InsertIncident(model.Incident) error               { return nil }
func (s *memSink) InsertDefect(model.Defect) error                   { return nil }
func (s *memSink) InsertFileMetric(model.FileMetric) error           { return nil }
func (s *memSink) InsertHarnessArtifact(model.HarnessArtifact) error { return nil }
func (s *memSink) InsertFileComplexityHistory(model.FileComplexityHistory) error {
	return nil
}
func (s *memSink) InsertRepoFile(model.RepoFile) error { return nil }

// newTestConnector constructs a connector wired against the supplied
// httptest server. The returned connector uses a stub token; tests inject
// the server URL via setBaseURL.
func newTestConnector(t *testing.T, srv *httptest.Server) *Connector {
	t.Helper()
	c, err := New(config.GitHubConn{Token: "test-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.setBaseURL(srv.URL); err != nil {
		t.Fatalf("setBaseURL: %v", err)
	}
	return c
}

// prGraphQLResponse builds a GraphQL response body matching the prListQuery
// shape with the supplied PR nodes embedded. The shurcooL/graphql jsonutil
// is forgiving — it reads only fields declared on the destination struct —
// so the test bodies set only the fields each test actually asserts on.
type prNodeJSON struct {
	Number                  int            `json:"number"`
	Title                   string         `json:"title"`
	Body                    string         `json:"body"`
	CreatedAt               string         `json:"createdAt"`
	MergedAt                *string        `json:"mergedAt,omitempty"`
	ClosedAt                *string        `json:"closedAt,omitempty"`
	UpdatedAt               string         `json:"updatedAt"`
	IsDraft                 bool           `json:"isDraft"`
	Additions               int            `json:"additions"`
	Deletions               int            `json:"deletions"`
	ChangedFiles            int            `json:"changedFiles"`
	BaseRefName             string         `json:"baseRefName"`
	HeadRefName             string         `json:"headRefName"`
	MergeCommit             *oidNode       `json:"mergeCommit,omitempty"`
	HeadRefOid              string         `json:"headRefOid"`
	Author                  loginNode      `json:"author"`
	HeadRepository          repoNameNode   `json:"headRepository"`
	Commits                 commitConn     `json:"commits"`
	Labels                  labelConn      `json:"labels"`
	ClosingIssuesReferences totalCountNode `json:"closingIssuesReferences"`
	TimelineItems           timelineConn   `json:"timelineItems"`
}

type oidNode struct {
	Oid string `json:"oid"`
}
type loginNode struct {
	Login string `json:"login"`
}
type repoNameNode struct {
	NameWithOwner string `json:"nameWithOwner"`
}
type commitConn struct {
	TotalCount int                  `json:"totalCount"`
	PageInfo   pageInfoNode         `json:"pageInfo"`
	Nodes      []commitConnNodeWrap `json:"nodes"`
}
type commitConnNodeWrap struct {
	Commit oidNode `json:"commit"`
}
type labelConn struct {
	PageInfo pageInfoNode `json:"pageInfo"`
	Nodes    []labelNode  `json:"nodes"`
}
type labelNode struct {
	Name string `json:"name"`
}
type totalCountNode struct {
	TotalCount int `json:"totalCount"`
}
type pageInfoNode struct {
	EndCursor   string `json:"endCursor"`
	HasNextPage bool   `json:"hasNextPage"`
}
type timelineConn struct {
	PageInfo pageInfoNode       `json:"pageInfo"`
	Nodes    []timelineItemJSON `json:"nodes"`
}
type timelineItemJSON struct {
	Typename  string `json:"__typename"`
	CreatedAt string `json:"createdAt,omitempty"`
	State     string `json:"state,omitempty"`
}

// buildPRListResponse wraps the nodes in the GraphQL response envelope the
// shurcooL/graphql client expects: {"data": { repository: { pullRequests: { ... } } }}.
func buildPRListResponse(nodes []prNodeJSON) string {
	payload := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"pageInfo": map[string]any{"endCursor": "", "hasNextPage": false},
					"nodes":    nodes,
				},
			},
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// runExtractPRs invokes extractPRs against the connector and returns the
// populated sink + provenance.
func runExtractPRs(t *testing.T, c *Connector, slug string, window connector.Window) (*memSink, *connector.Provenance) {
	t.Helper()
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), slug, window)
	c.extractPRs(context.Background(), connector.Repo{Slug: slug}, window, sink, &prov)
	return sink, &prov
}

func standardWindow() connector.Window {
	return connector.Window{
		Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC),
	}
}

// graphqlBodyContains returns true if the GraphQL request body contains
// the supplied substring (e.g. a specific PR number or paginated cursor).
// Used by tests that route on the request body rather than the URL path.
func graphqlBodyContains(r *http.Request, want string) bool {
	body, _ := io.ReadAll(r.Body)
	// Restore for downstream handlers.
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return strings.Contains(string(body), want)
}

// emptyJSONOK returns "{}" with a 200; used for REST endpoints the test
// does not care about but which the connector still calls.
func emptyJSONOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

func emptyJSONArrayOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`[]`))
}

// ----------------------------------------------------------------------
// Test bodies.
// ----------------------------------------------------------------------

func TestExtractPRs_BasicShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Bulk prListQuery uses pullRequests(plural); per-PR overflow
		// paginators use pullRequest(number:). Differentiate on that.
		if graphqlBodyContains(r, "pullRequest(number:") {
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{}}}}`)
			return
		}
		fmt.Fprintln(w, buildPRListResponse([]prNodeJSON{{
			Number:         42,
			Title:          "Add new endpoint",
			Body:           "## Summary\nDoes a thing\n",
			CreatedAt:      "2025-03-01T00:00:00Z",
			UpdatedAt:      "2025-03-02T00:00:00Z",
			BaseRefName:    "main",
			HeadRefName:    "feature",
			HeadRefOid:     "abc",
			Author:         loginNode{Login: "alice"},
			HeadRepository: repoNameNode{NameWithOwner: "kmcd/foo"},
			Commits: commitConn{
				TotalCount: 1,
				Nodes:      []commitConnNodeWrap{{Commit: oidNode{Oid: "deadbeef"}}},
			},
			ClosingIssuesReferences: totalCountNode{TotalCount: 0},
		}}))
	})
	// REST endpoints reachable from emitPR.
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/repos/kmcd/foo/pulls/42", func(w http.ResponseWriter, _ *http.Request) {
		// Unmerged: deriveMergeMethod returns "" via fetchMergeMethod's
		// early return.
		fmt.Fprintln(w, `{"number":42,"merged":false}`)
	})
	mux.HandleFunc("/repos/kmcd/foo/pulls/42/reviews", emptyJSONArrayOK)
	mux.HandleFunc("/repos/kmcd/foo/pulls/42/comments", emptyJSONArrayOK)
	mux.HandleFunc("/repos/kmcd/foo/issues/42/comments", emptyJSONArrayOK)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

	if len(sink.prs) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
	}
	row := sink.prs[0]
	if row.Number != 42 || row.Title != "Add new endpoint" || row.AuthorHandle != hashHandle(canonicalLogin("alice")) {
		t.Errorf("unexpected row: %+v", row)
	}
	if row.HasRiskMarker {
		t.Errorf("expected has_risk_marker false for clean body")
	}
	if row.BodyLength == 0 {
		t.Errorf("expected non-zero body length")
	}
	if row.CommitCount != 1 {
		t.Errorf("commit_count = %d, want 1", row.CommitCount)
	}
	if row.IssueRefsCount != 0 {
		t.Errorf("issue_refs_count = %d, want 0", row.IssueRefsCount)
	}
	if len(sink.prCommits) != 1 || sink.prCommits[0].SHA != "deadbeef" {
		t.Errorf("pr_commits = %+v", sink.prCommits)
	}
}

func TestExtractPRs_RiskMarker(t *testing.T) {
	srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
		Number:    7,
		Title:     "Quick fix",
		Body:      "We need a hotfix for the auth bug.",
		CreatedAt: "2025-03-01T00:00:00Z",
		UpdatedAt: "2025-03-02T00:00:00Z",
		Author:    loginNode{Login: "bob"},
	}}, nil, nil, false))
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

	if len(sink.prs) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
	}
	if !sink.prs[0].HasRiskMarker {
		t.Errorf("expected has_risk_marker=true for body containing 'hotfix'")
	}
}

func TestExtractPRs_Draft(t *testing.T) {
	srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
		Number:    8,
		Title:     "WIP feature",
		Body:      "draft for review",
		CreatedAt: "2025-03-01T00:00:00Z",
		UpdatedAt: "2025-03-02T00:00:00Z",
		IsDraft:   true,
		Author:    loginNode{Login: "carol"},
	}}, nil, nil, false))
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

	if len(sink.prs) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
	}
	if !sink.prs[0].IsDraft {
		t.Errorf("expected is_draft=true")
	}
}

func TestExtractPRs_TimelineForcePushAfterReview(t *testing.T) {
	t.Run("force_push_after_review", func(t *testing.T) {
		srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
			Number:    9,
			Title:     "feat",
			Body:      "body",
			CreatedAt: "2025-03-01T00:00:00Z",
			UpdatedAt: "2025-03-10T00:00:00Z",
			Author:    loginNode{Login: "dan"},
			TimelineItems: timelineConn{
				Nodes: []timelineItemJSON{
					{Typename: "PullRequestReview", CreatedAt: "2025-03-05T00:00:00Z", State: "APPROVED"},
					{Typename: "HeadRefForcePushedEvent", CreatedAt: "2025-03-06T00:00:00Z"},
				},
			},
		}}, nil, nil, false))
		defer srv.Close()

		c := newTestConnector(t, srv)
		sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

		if len(sink.prs) != 1 {
			t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
		}
		if !sink.prs[0].ForcePushedAfterReview {
			t.Errorf("expected force_pushed_after_review=true")
		}
	})

	t.Run("force_push_before_review", func(t *testing.T) {
		srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
			Number:    10,
			Title:     "feat",
			Body:      "body",
			CreatedAt: "2025-03-01T00:00:00Z",
			UpdatedAt: "2025-03-10T00:00:00Z",
			Author:    loginNode{Login: "ev"},
			TimelineItems: timelineConn{
				Nodes: []timelineItemJSON{
					{Typename: "HeadRefForcePushedEvent", CreatedAt: "2025-03-03T00:00:00Z"},
					{Typename: "PullRequestReview", CreatedAt: "2025-03-05T00:00:00Z", State: "APPROVED"},
				},
			},
		}}, nil, nil, false))
		defer srv.Close()

		c := newTestConnector(t, srv)
		sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

		if len(sink.prs) != 1 {
			t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
		}
		if sink.prs[0].ForcePushedAfterReview {
			t.Errorf("expected force_pushed_after_review=false")
		}
	})
}

func TestExtractPRs_TemplateMatch(t *testing.T) {
	t.Run("matches_template", func(t *testing.T) {
		template := "## Summary\n\n## Test plan\n"
		// base64 of template via go-github contents API.
		srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
			Number:    11,
			Title:     "feat",
			Body:      "## Summary\n\nshipping it\n",
			CreatedAt: "2025-03-01T00:00:00Z",
			UpdatedAt: "2025-03-10T00:00:00Z",
			Author:    loginNode{Login: "fey"},
		}}, &template, nil, false))
		defer srv.Close()

		c := newTestConnector(t, srv)
		sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

		if len(sink.prs) != 1 {
			t.Fatalf("expected 1 PR, got %d", len(sink.prs))
		}
		if sink.prs[0].TemplateMatch == nil {
			t.Fatalf("expected non-nil template_match")
		}
		if *sink.prs[0].TemplateMatch != 0.5 {
			t.Errorf("template_match = %v, want 0.5", *sink.prs[0].TemplateMatch)
		}
	})

	t.Run("template_missing", func(t *testing.T) {
		srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
			Number:    12,
			Title:     "feat",
			Body:      "## Summary\n\nshipping it\n",
			CreatedAt: "2025-03-01T00:00:00Z",
			UpdatedAt: "2025-03-10T00:00:00Z",
			Author:    loginNode{Login: "gus"},
		}}, nil, nil, false))
		defer srv.Close()

		c := newTestConnector(t, srv)
		sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

		if len(sink.prs) != 1 {
			t.Fatalf("expected 1 PR, got %d", len(sink.prs))
		}
		if sink.prs[0].TemplateMatch != nil {
			t.Errorf("expected nil template_match when template is absent; got %v", *sink.prs[0].TemplateMatch)
		}
	})
}

func TestExtractPRs_ClosingIssuesReferences(t *testing.T) {
	srv := httptest.NewServer(prsMux(t, []prNodeJSON{{
		Number:                  13,
		Title:                   "feat",
		Body:                    "body without refs",
		CreatedAt:               "2025-03-01T00:00:00Z",
		UpdatedAt:               "2025-03-02T00:00:00Z",
		Author:                  loginNode{Login: "hal"},
		ClosingIssuesReferences: totalCountNode{TotalCount: 3},
	}}, nil, nil, false))
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

	if len(sink.prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(sink.prs))
	}
	if got := sink.prs[0].IssueRefsCount; got != 3 {
		t.Errorf("issue_refs_count = %d, want 3", got)
	}
}

func TestExtractBranches_ProtectionAccessible(t *testing.T) {
	clone := setupCloneWithRemoteRefs(t, []string{"main"})

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"data":{"repository":{"branchProtectionRules":{
			"pageInfo":{"endCursor":"","hasNextPage":false},
			"nodes":[{
				"requiresApprovingReviews":true,
				"requiredApprovingReviewCount":2,
				"isAdminEnforced":true,
				"restrictsPushes":false,
				"requiredStatusCheckContexts":[],
				"matchingRefs":{"nodes":[{"name":"main"}]}
			}]
		}}}}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractBranches(context.Background(), connector.Repo{Slug: "kmcd/foo", DefaultBranch: "main", Clone: clone}, sink, &prov)

	if len(sink.branches) != 1 {
		t.Fatalf("expected 1 branch row, got %d", len(sink.branches))
	}
	if !sink.branches[0].IsDefault {
		t.Errorf("expected branches[0].is_default=true for 'main'")
	}
	if len(sink.branchProtection) != 1 {
		t.Fatalf("expected 1 branch_protection row, got %d", len(sink.branchProtection))
	}
	if sink.branchProtection[0].RequiredReviews == nil || *sink.branchProtection[0].RequiredReviews != 2 {
		t.Errorf("required_reviews = %+v, want 2", sink.branchProtection[0].RequiredReviews)
	}
	ep := prov.Endpoints["branch_protection"]
	if !ep.Accessible {
		t.Errorf("expected endpoints[branch_protection].Accessible = true, got %+v", ep)
	}
	// Pin RowsReturned counters — a `++ → --` mutation on the branches.go
	// emit sites flips these to -N. The sink-shape assertion above wouldn't
	// catch that.
	if got, want := prov.RowsReturned["branches"], len(sink.branches); got != want {
		t.Errorf("RowsReturned[branches] = %d, want %d", got, want)
	}
	if got, want := prov.RowsReturned["branch_protection"], len(sink.branchProtection); got != want {
		t.Errorf("RowsReturned[branch_protection] = %d, want %d", got, want)
	}
}

func TestExtractBranches_Protection403(t *testing.T) {
	clone := setupCloneWithRemoteRefs(t, []string{"main"})

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractBranches(context.Background(), connector.Repo{Slug: "kmcd/foo", DefaultBranch: "main", Clone: clone}, sink, &prov)

	if len(sink.branches) != 1 {
		t.Fatalf("expected 1 branch row, got %d", len(sink.branches))
	}
	if len(sink.branchProtection) != 0 {
		t.Fatalf("expected 0 branch_protection rows on 403, got %d", len(sink.branchProtection))
	}
	ep, ok := prov.Endpoints["branch_protection"]
	if !ok {
		t.Fatalf("expected endpoints[branch_protection] entry")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected a Reason on inaccessible endpoint; got empty")
	}
}

func TestPing(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `{"login":"octocat"}`)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		c := newTestConnector(t, srv)
		if err := c.Ping(context.Background()); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"message":"bad creds"}`, http.StatusUnauthorized)
		})
		srv := httptest.NewServer(mux)
		defer srv.Close()

		c := newTestConnector(t, srv)
		if err := c.Ping(context.Background()); err == nil {
			t.Errorf("Ping: expected error on 401, got nil")
		}
	})
}

// prsMux builds the standard handler set used by the PR-shape tests: one
// GraphQL endpoint returning the supplied nodes plus minimal REST handlers
// for every endpoint emitPR touches. If template is non-nil, the contents
// endpoint returns it (base64-encoded); otherwise 404. If pulls is non-nil,
// it is used as the JSON response for every /pulls/{n} request; otherwise
// the PR is reported unmerged.
func prsMux(_ *testing.T, nodes []prNodeJSON, template *string, pulls map[int]string, _ bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		if graphqlBodyContains(r, "pullRequest(number:") {
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{}}}}`)
			return
		}
		fmt.Fprintln(w, buildPRListResponse(nodes))
	})
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		if template == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// go-github expects a file content response with base64-encoded content.
		payload := map[string]any{
			"name":     "PULL_REQUEST_TEMPLATE.md",
			"path":     ".github/PULL_REQUEST_TEMPLATE.md",
			"type":     "file",
			"encoding": "base64",
			"content":  base64Encode(*template),
		}
		b, _ := json.Marshal(payload)
		_, _ = w.Write(b)
	})

	// Generic /pulls/{n} handler: returns canned merged status from pulls
	// map or unmerged default.
	mux.HandleFunc("/repos/kmcd/foo/pulls/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Trim the prefix to extract the number / suffix.
		rest := strings.TrimPrefix(path, "/repos/kmcd/foo/pulls/")
		// Sub-paths like 42/reviews, 42/comments, etc.
		switch {
		case strings.HasSuffix(rest, "/reviews"), strings.HasSuffix(rest, "/comments"):
			emptyJSONArrayOK(w, r)
			return
		}
		// rest is a PR number
		n, err := strconv.Atoi(rest)
		if err == nil {
			if body, ok := pulls[n]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
			fmt.Fprintf(w, `{"number":%d,"merged":false}`, n)
			return
		}
		emptyJSONOK(w, r)
	})
	mux.HandleFunc("/repos/kmcd/foo/issues/", func(w http.ResponseWriter, r *http.Request) {
		emptyJSONArrayOK(w, r)
	})
	return mux
}

// base64Encode returns the base64 of s with newlines every 60 chars,
// matching the GitHub contents API line-wrap behaviour. go-github accepts
// either; we keep it simple.
func base64Encode(s string) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(s)
	n := len(src)
	out := make([]byte, 0, ((n+2)/3)*4)
	for i := 0; i < n; i += 3 {
		var b [3]byte
		copy(b[:], src[i:])
		out = append(out, tbl[b[0]>>2])
		out = append(out, tbl[((b[0]&0x03)<<4)|(b[1]>>4)])
		if i+1 < n {
			out = append(out, tbl[((b[1]&0x0F)<<2)|(b[2]>>6)])
		} else {
			out = append(out, '=')
		}
		if i+2 < n {
			out = append(out, tbl[b[2]&0x3F])
		} else {
			out = append(out, '=')
		}
	}
	return string(out)
}
