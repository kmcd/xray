package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// Additional in-memory sink fields needed by these tests live on a
// separate fixture struct so we can keep memSink focused on what http_test.go
// exercises. We use a small adapter type that delegates and records the
// extra tables.
type extraSink struct {
	memSink
	codeowners []model.Codeowner
	languages  []model.RepoLanguage
	releases   []model.Release
	deploys    []model.Deploy
	reviews    []model.Review
	comments   []model.PRComment
	reqs       []model.PRReviewRequest
}

func (s *extraSink) InsertCodeowner(c model.Codeowner) error {
	s.codeowners = append(s.codeowners, c)
	return nil
}
func (s *extraSink) InsertRepoLanguage(l model.RepoLanguage) error {
	s.languages = append(s.languages, l)
	return nil
}
func (s *extraSink) InsertRelease(r model.Release) error {
	s.releases = append(s.releases, r)
	return nil
}
func (s *extraSink) InsertDeploy(d model.Deploy) error {
	s.deploys = append(s.deploys, d)
	return nil
}
func (s *extraSink) InsertReview(r model.Review) error {
	s.reviews = append(s.reviews, r)
	return nil
}
func (s *extraSink) InsertPRComment(c model.PRComment) error {
	s.comments = append(s.comments, c)
	return nil
}
func (s *extraSink) InsertPRReviewRequest(r model.PRReviewRequest) error {
	s.reqs = append(s.reqs, r)
	return nil
}

func TestExtractCodeowners(t *testing.T) {
	// Connector reads CODEOWNERS off the local clone (#72). Lay the file
	// down at the highest-priority candidate path and confirm parseCodeowners
	// emits the expected rows.
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	content := "*.go @alice @kmcd/backend\n# comment line\n\n*.md @bob\n"
	if err := os.WriteFile(filepath.Join(clone, ".github/CODEOWNERS"), []byte(content), 0o644); err != nil {
		t.Fatalf("write CODEOWNERS: %v", err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractCodeowners(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, sink, &prov)

	if len(sink.codeowners) != 3 {
		t.Fatalf("expected 3 codeowners rows, got %d: %+v", len(sink.codeowners), sink.codeowners)
	}
	gotUser, gotTeam := 0, 0
	for _, r := range sink.codeowners {
		switch r.OwnerType {
		case "user":
			gotUser++
		case "team":
			gotTeam++
		}
	}
	if gotUser != 2 || gotTeam != 1 {
		t.Errorf("user/team counts = %d/%d, want 2/1", gotUser, gotTeam)
	}
	// Pin RowsReturned — kills the `++ → --` mutation on codeowners.go:63.
	if got, want := prov.RowsReturned["codeowners"], len(sink.codeowners); got != want {
		t.Errorf("RowsReturned[codeowners] = %d, want %d", got, want)
	}
	if ep := prov.Endpoints["codeowners"]; !ep.Accessible {
		t.Errorf("expected codeowners endpoint Accessible=true, got %+v", ep)
	}
}

func TestExtractCodeowners_PathPriority(t *testing.T) {
	// First-match-wins across the candidate list: .github/CODEOWNERS over
	// CODEOWNERS at the repo root.
	clone := t.TempDir()
	if err := os.MkdirAll(filepath.Join(clone, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".github/CODEOWNERS"), []byte("*.go @primary\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, "CODEOWNERS"), []byte("*.rb @fallback\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractCodeowners(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, sink, &prov)

	if len(sink.codeowners) != 1 || sink.codeowners[0].OwnerHandle != "primary" {
		t.Errorf("expected only .github/CODEOWNERS rule (owner=primary), got %+v", sink.codeowners)
	}
}

func TestExtractCodeowners_Missing(t *testing.T) {
	clone := t.TempDir()
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractCodeowners(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, sink, &prov)

	if len(sink.codeowners) != 0 {
		t.Errorf("expected 0 rows when CODEOWNERS absent, got %+v", sink.codeowners)
	}
	if ep := prov.Endpoints["codeowners"]; !ep.Accessible {
		t.Errorf("missing CODEOWNERS still records endpoint Accessible=true; got %+v", ep)
	}
}

func TestExtractReleases(t *testing.T) {
	// Three releases: one inside the window, one before, one after. Only
	// the in-window release should land. The in-window release uses tag
	// resolution (#57), the older one's request is expected because the
	// connector keeps paging until it sees the boundary.
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/releases", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.0.0","name":"future","created_at":"2026-03-01T00:00:00Z","target_commitish":"main","prerelease":false},
			{"tag_name":"v1.0.0","name":"in-window","created_at":"2025-06-15T00:00:00Z","target_commitish":"main","prerelease":false},
			{"tag_name":"v0.9.0","name":"ancient","created_at":"2024-06-15T00:00:00Z","target_commitish":"main","prerelease":true}
		]`))
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/v2.0.0", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ffffffffffffffffffffffffffffffffffffffff"))
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/v1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1111111111111111111111111111111111111111"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if len(sink.releases) != 1 {
		t.Fatalf("expected 1 release inside window, got %d: %+v", len(sink.releases), sink.releases)
	}
	if got := sink.releases[0].Tag; got != "v1.0.0" {
		t.Errorf("wrong release tag landed: %q", got)
	}
	if got := sink.releases[0].SHA; got != "1111111111111111111111111111111111111111" {
		t.Errorf("release SHA = %q; want the tag-resolved SHA (issue #57), not HEAD-of-main", got)
	}
	if len(sink.deploys) != 1 {
		t.Errorf("expected 1 deploy, got %d", len(sink.deploys))
	}
	if ep := prov.Endpoints["releases"]; !ep.Accessible {
		t.Errorf("expected endpoints[releases].Accessible=true after clean walk, got %+v", ep)
	}
	// Pin RowsReturned — kills the `++ → --` mutation on releases.go.
	if got, want := prov.RowsReturned["releases"], len(sink.releases); got != want {
		t.Errorf("RowsReturned[releases] = %d, want %d", got, want)
	}
	if got, want := prov.RowsReturned["deploys"], len(sink.deploys); got != want {
		t.Errorf("RowsReturned[deploys] = %d, want %d", got, want)
	}
}

func TestExtractReleases_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/releases", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if len(sink.releases) != 0 {
		t.Errorf("expected 0 release rows on 403, got %d", len(sink.releases))
	}
	ep, ok := prov.Endpoints["releases"]
	if !ok {
		t.Fatalf("expected endpoints[releases] entry on 403")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on 403; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on 403; got empty")
	}
	if prov.RowsReturned["releases"] != 0 {
		t.Errorf("expected RowsReturned[releases]=0 on 403; got %d", prov.RowsReturned["releases"])
	}
}

func TestRequestedIdentity(t *testing.T) {
	cases := []struct {
		name string
		ev   reviewRequestedEvent
		want struct{ h, t string }
	}{
		{
			name: "user",
			ev:   func() reviewRequestedEvent { var e reviewRequestedEvent; e.RequestedReviewer.User.Login = "alice"; return e }(),
			want: struct{ h, t string }{"alice", "user"},
		},
		{
			name: "team",
			ev:   func() reviewRequestedEvent { var e reviewRequestedEvent; e.RequestedReviewer.Team.CombinedSlug = "org/team"; return e }(),
			want: struct{ h, t string }{"org/team", "team"},
		},
		{
			name: "empty",
			ev:   reviewRequestedEvent{},
			want: struct{ h, t string }{"", ""},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, ty := requestedIdentity(c.ev)
			if h != c.want.h || ty != c.want.t {
				t.Errorf("requestedIdentity = (%q, %q), want (%q, %q)", h, ty, c.want.h, c.want.t)
			}
		})
	}
}

// TestExtractPRs_BulkEnrichment exercises the new inline-GraphQL path:
// reviews, comments, review threads, and review requests are all carried
// in the prListQuery response and emit rows from emitPR without any
// follow-up round-trip.
func TestExtractPRs_BulkEnrichment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Any per-PR overflow query: empty data.
		if graphqlBodyContains(r, "pullRequest(number:") {
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{}}}}`)
			return
		}
		// PR list: one PR with one of each inner thing.
		body := `{"data":{"repository":{"pullRequests":{
			"pageInfo":{"endCursor":"","hasNextPage":false},
			"nodes":[{
				"number":42,
				"title":"Feature",
				"body":"## Summary\nWork\n",
				"createdAt":"2025-03-01T00:00:00Z",
				"updatedAt":"2025-03-05T00:00:00Z",
				"baseRefName":"main",
				"headRefName":"feature",
				"headRefOid":"abc",
				"author":{"login":"alice"},
				"headRepository":{"nameWithOwner":"kmcd/foo"},
				"commits":{"totalCount":1,"pageInfo":{"endCursor":"","hasNextPage":false},"nodes":[{"commit":{"oid":"deadbeef"}}]},
				"closingIssuesReferences":{"totalCount":0},
				"timelineItems":{
					"pageInfo":{"endCursor":"","hasNextPage":false},
					"nodes":[
						{"__typename":"ReviewRequestedEvent","createdAt":"2025-03-02T00:00:00Z","requestedReviewer":{"login":"reviewerA"}}
					]
				},
				"reviews":{
					"totalCount":1,
					"pageInfo":{"endCursor":"","hasNextPage":false},
					"nodes":[
						{"state":"APPROVED","submittedAt":"2025-03-03T00:00:00Z","body":"lgtm","author":{"login":"reviewerA"}}
					]
				},
				"comments":{
					"totalCount":1,
					"pageInfo":{"endCursor":"","hasNextPage":false},
					"nodes":[
						{"author":{"login":"bob"},"createdAt":"2025-03-04T00:00:00Z","body":"nice"}
					]
				},
				"reviewThreads":{
					"totalCount":1,
					"pageInfo":{"endCursor":"","hasNextPage":false},
					"nodes":[
						{"comments":{"totalCount":1,"pageInfo":{"endCursor":"","hasNextPage":false},"nodes":[
							{"author":{"login":"carol"},"createdAt":"2025-03-04T12:00:00Z","body":"nit","path":"foo.go","databaseId":100,"replyTo":null}
						]}}
					]
				},
				"labels":{
					"nodes":[
						{"name":"bug"},
						{"name":"area/connectors"}
					]
				}
			}]
		}}}}`
		fmt.Fprintln(w, body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if len(sink.prs) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
	}
	if len(sink.reviews) != 1 {
		t.Errorf("expected 1 review row, got %d: %+v", len(sink.reviews), sink.reviews)
	}
	if len(sink.comments) != 2 {
		t.Errorf("expected 2 pr_comments rows (1 issue, 1 review), got %d: %+v", len(sink.comments), sink.comments)
	}
	kinds := map[string]int{}
	for _, cm := range sink.comments {
		kinds[cm.Kind]++
	}
	if kinds["issue_comment"] != 1 || kinds["review_comment"] != 1 {
		t.Errorf("comment kinds = %v, want issue:1 review:1", kinds)
	}
	if len(sink.reqs) != 1 {
		t.Errorf("expected 1 pr_review_requests row, got %d: %+v", len(sink.reqs), sink.reqs)
	}
	if sink.reviews[0].ReviewerHandle != hashHandle(canonicalLogin("reviewerA")) {
		t.Errorf("reviewer_handle = %q, want hashed reviewerA", sink.reviews[0].ReviewerHandle)
	}
	if sink.prs[0].FirstReviewAt == nil {
		t.Errorf("expected first_review_at populated from inline reviews")
	}
	// Pin RowsReturned for every PR-derived emission — kills the lived
	// `++ → --` mutations on prs.go:566/619/677, pr_comments.go:33/71,
	// pr_meta.go:83, reviews.go:41. Sink-shape assertions above wouldn't
	// catch a counter flip on its own.
	wantCounters := map[string]int{
		"prs":                len(sink.prs),
		"reviews":            len(sink.reviews),
		"pr_comments":        len(sink.comments),
		"pr_review_requests": len(sink.reqs),
		"pr_commits":         len(sink.prCommits),
		"pr_labels":          len(sink.prLabels),
	}
	for k, want := range wantCounters {
		if got := prov.RowsReturned[k]; got != want {
			t.Errorf("RowsReturned[%s] = %d, want %d", k, got, want)
		}
	}
}

// TestExtractPRs_InlineOverflow_Reviews exercises the review-overflow
// paginator: the PR-list response signals HasNextPage=true on the inline
// Reviews connection, and the connector follows up with paginatePRReviewsOverflow.
func TestExtractPRs_InlineOverflow_Reviews(t *testing.T) {
	overflowHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		if graphqlBodyContains(r, "pullRequest(number:") {
			overflowHits++
			// One more review on overflow.
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{
				"reviews":{
					"pageInfo":{"endCursor":"","hasNextPage":false},
					"nodes":[
						{"state":"APPROVED","submittedAt":"2025-03-04T00:00:00Z","body":"second","author":{"login":"second"}}
					]
				}
			}}}}`)
			return
		}
		fmt.Fprintln(w, `{"data":{"repository":{"pullRequests":{
			"pageInfo":{"endCursor":"","hasNextPage":false},
			"nodes":[{
				"number":50,
				"title":"Big PR",
				"body":"body",
				"createdAt":"2025-03-01T00:00:00Z",
				"updatedAt":"2025-03-05T00:00:00Z",
				"author":{"login":"alice"},
				"commits":{"totalCount":1,"pageInfo":{"endCursor":"","hasNextPage":false},"nodes":[]},
				"reviews":{
					"pageInfo":{"endCursor":"cursorA","hasNextPage":true},
					"nodes":[
						{"state":"APPROVED","submittedAt":"2025-03-03T00:00:00Z","body":"first","author":{"login":"first"}}
					]
				}
			}]
		}}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if overflowHits != 1 {
		t.Errorf("expected 1 per-PR overflow GraphQL call, got %d", overflowHits)
	}
	if len(sink.reviews) != 2 {
		t.Fatalf("expected 2 review rows (1 inline + 1 overflow), got %d: %+v", len(sink.reviews), sink.reviews)
	}
}

// TestExtractPRs_InlineOverflow_Commits drives the paginatePRCommits
// overflow path so its RowsReturned[pr_commits]++ at prs.go:677 runs
// under test. The inline page signals hasNextPage=true with one commit;
// the follow-up paginatePRCommits query returns one more, terminating.
func TestExtractPRs_InlineOverflow_Commits(t *testing.T) {
	overflowHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		// The overflow shape carries a `commits(first: 100, after:` block.
		if graphqlBodyContains(r, "pullRequest(number:") && graphqlBodyContains(r, "commits(first") {
			overflowHits++
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{
				"commits":{
					"pageInfo":{"endCursor":"","hasNextPage":false},
					"nodes":[
						{"commit":{"oid":"cafef00d"}}
					]
				}
			}}}}`)
			return
		}
		fmt.Fprintln(w, `{"data":{"repository":{"pullRequests":{
			"pageInfo":{"endCursor":"","hasNextPage":false},
			"nodes":[{
				"number":77,
				"title":"Wide PR",
				"body":"body",
				"createdAt":"2025-03-01T00:00:00Z",
				"updatedAt":"2025-03-05T00:00:00Z",
				"author":{"login":"alice"},
				"commits":{
					"totalCount":2,
					"pageInfo":{"endCursor":"cursorC","hasNextPage":true},
					"nodes":[{"commit":{"oid":"deadbeef"}}]
				}
			}]
		}}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if overflowHits != 1 {
		t.Errorf("expected 1 per-PR commits overflow GraphQL call, got %d", overflowHits)
	}
	if got, want := len(sink.prCommits), 2; got != want {
		t.Fatalf("pr_commits rows = %d, want %d (1 inline + 1 overflow)", got, want)
	}
	// Pin the overflow-path counter — kills the `++ → --` mutation at
	// prs.go:677 (paginatePRCommits). The inline-path counter increment
	// at prs.go:566 is covered by TestExtractPRs_BulkEnrichment.
	if got, want := prov.RowsReturned["pr_commits"], 2; got != want {
		t.Errorf("RowsReturned[pr_commits] = %d, want %d", got, want)
	}
}

// TestCostInterceptor_UpdatesGQLBudget verifies that a GraphQL response
// carrying throttleStatus.remaining updates the ratelimit transport's
// "github-graphql" budget snapshot. This exercises the costInterceptor →
// Transport.UpdateGQLBudget wiring added in #139.
func TestCostInterceptor_UpdatesGQLBudget(t *testing.T) {
	resetAt := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second)
	resetUnix := strconv.FormatInt(resetAt.Unix(), 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-RateLimit-Reset", resetUnix)
		fmt.Fprintf(w, `{"data":{},"extensions":{"cost":{"requestedQueryCost":1,"actualQueryCost":1,"throttleStatus":{"remaining":1200}}}}`)
	}))
	defer srv.Close()

	c := newTestConnector(t, srv)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/graphql",
		http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	snap := c.rl.Snapshot()
	st, ok := snap["github-graphql"]
	if !ok {
		ks := make([]string, 0, len(snap))
		for k := range snap {
			ks = append(ks, k)
		}
		t.Fatalf("expected 'github-graphql' in Snapshot after GQL response with cost data; got keys: %v", ks)
	}
	if st.Remaining != 1200 {
		t.Errorf("github-graphql Remaining = %d, want 1200", st.Remaining)
	}
	if !st.HasRemaining {
		t.Errorf("github-graphql HasRemaining = false, want true")
	}
	if st.Limit != 5000 {
		t.Errorf("github-graphql Limit = %d, want 5000", st.Limit)
	}
	if !st.ResetAt.Equal(resetAt) {
		t.Errorf("github-graphql ResetAt = %v, want %v", st.ResetAt, resetAt)
	}
}

// TestCostInterceptor_AbsentThrottleStatusNoSpuriousPacing verifies that a
// GraphQL response where throttleStatus is absent (remaining zero-initialises
// to 0) does NOT trigger SetGQLPacing. Before the fix, `0 < gqlLowWaterMark`
// was true and combined with the absent X-RateLimit-Reset header would cause
// a ~1h pacing sleep.
func TestCostInterceptor_AbsentThrottleStatusNoSpuriousPacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// No X-RateLimit-Reset header; throttleStatus absent from body.
		fmt.Fprintf(w, `{"data":{},"extensions":{"cost":{"requestedQueryCost":1,"actualQueryCost":1}}}`)
	}))
	defer srv.Close()

	c := newTestConnector(t, srv)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/graphql",
		http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	// Second request must not stall — if SetGQLPacing fired spuriously, the
	// sleep would be ~1h and this call would time out.
	req2, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/graphql",
		http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest 2: %v", err)
	}
	done := make(chan struct{})
	go func() {
		resp2, err2 := c.httpClient.Do(req2)
		if err2 == nil {
			resp2.Body.Close()
		}
		close(done)
	}()
	select {
	case <-done:
		// fast — no spurious pacing
	case <-time.After(2 * time.Second):
		t.Error("second request stalled: SetGQLPacing was triggered spuriously on absent throttleStatus")
	}

	// "github-graphql" must NOT appear in Snapshot — remaining was 0 (absent).
	snap := c.rl.Snapshot()
	if _, ok := snap["github-graphql"]; ok {
		t.Error("Snapshot should not contain 'github-graphql' when throttleStatus was absent")
	}
}
