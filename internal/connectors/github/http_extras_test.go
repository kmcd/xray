package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

func TestExtractLanguages(t *testing.T) {
	// extractLanguages walks the local clone (#72) and classifies files with
	// go-enry. Lay down files with extensions enry recognises and assert
	// one row per language with on-disk byte counts.
	clone := t.TempDir()
	files := map[string]string{
		"main.go":   "package main\n\nfunc main() {}\n",
		"helper.go": "package main\n\nfunc h() {}\n",
		"app.rb":    "puts 'hi'\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(clone, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	if err := c.extractLanguages(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, sink, &prov); err != nil {
		t.Fatalf("extractLanguages: %v", err)
	}
	byLang := map[string]int64{}
	for _, r := range sink.languages {
		byLang[r.Language] = r.Bytes
	}
	if byLang["Go"] == 0 {
		t.Errorf("expected Go byte count > 0; got rows %+v", sink.languages)
	}
	if byLang["Ruby"] == 0 {
		t.Errorf("expected Ruby byte count > 0; got rows %+v", sink.languages)
	}
	// Bytes must equal the sum of file sizes per language.
	wantGo := int64(len(files["main.go"]) + len(files["helper.go"]))
	if byLang["Go"] != wantGo {
		t.Errorf("Go bytes = %d, want %d (sum of file sizes)", byLang["Go"], wantGo)
	}
}

func TestExtractLanguages_EmptyClone(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	if err := c.extractLanguages(context.Background(), connector.Repo{Slug: "kmcd/foo"}, sink, &prov); err != nil {
		t.Fatalf("extractLanguages: %v", err)
	}
	if len(sink.languages) != 0 {
		t.Errorf("expected 0 rows when Clone is empty; got %+v", sink.languages)
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

func TestIsFullSHA(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0123456789abcdef0123456789abcdef01234567", true},
		{"0123456789ABCDEF0123456789ABCDEF01234567", true},
		{"main", false},
		{"", false},
		{"0123456789abcdef0123456789abcdef0123456", false}, // 39 chars
		{"0123456789abcdef0123456789abcdef012345678", false},
		{"zzzz56789abcdef0123456789abcdef0123456788", false},
	}
	for _, c := range cases {
		if got := isFullSHA(c.in); got != c.want {
			t.Errorf("isFullSHA(%q) = %v, want %v", c.in, got, c.want)
		}
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
