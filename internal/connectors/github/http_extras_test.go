package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	mux := http.NewServeMux()
	// Probes .github/CODEOWNERS first; serve a file there.
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/CODEOWNERS", func(w http.ResponseWriter, _ *http.Request) {
		content := "*.go @alice @kmcd/backend\n# comment line\n\n*.md @bob\n"
		body := fmt.Sprintf(`{"name":"CODEOWNERS","type":"file","encoding":"base64","content":"%s"}`, base64Encode(content))
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractCodeowners(context.Background(), connector.Repo{Slug: "kmcd/foo"}, sink, &prov)

	if len(sink.codeowners) != 3 {
		t.Fatalf("expected 3 codeowners rows, got %d: %+v", len(sink.codeowners), sink.codeowners)
	}
	// Verify one user + one team classification.
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
}

func TestExtractLanguages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/languages", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Go":12345,"Ruby":678}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	if err := c.extractLanguages(context.Background(), connector.Repo{Slug: "kmcd/foo"}, sink, &prov); err != nil {
		t.Fatalf("extractLanguages: %v", err)
	}
	if len(sink.languages) != 2 {
		t.Fatalf("expected 2 language rows, got %d", len(sink.languages))
	}
}

func TestExtractReleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/releases", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v1.0.0","name":"first","created_at":"2025-03-01T00:00:00Z","target_commitish":"0123456789abcdef0123456789abcdef01234567","prerelease":false},
			{"tag_name":"v0.9.0","name":"prev","created_at":"2025-02-01T00:00:00Z","target_commitish":"main","prerelease":true}
		]`))
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/main", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Accept", "application/vnd.github.sha")
		_, _ = w.Write([]byte("abcdef0123456789abcdef0123456789abcdef01"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, sink, &prov)
	if len(sink.releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(sink.releases))
	}
	if len(sink.deploys) != 2 {
		t.Errorf("expected 2 deploys, got %d", len(sink.deploys))
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

func TestExtractReviews(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"state":"APPROVED","submitted_at":"2025-03-02T00:00:00Z","user":{"login":"alice"},"body":"lgtm"},
			{"state":"PENDING","submitted_at":"2025-03-03T00:00:00Z","user":{"login":"bob"},"body":"draft"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	first := c.extractReviews(context.Background(), connector.Repo{Slug: "kmcd/foo"}, 5, sink, &prov)
	if len(sink.reviews) != 1 {
		t.Fatalf("expected 1 review (PENDING excluded), got %d", len(sink.reviews))
	}
	if first == nil {
		t.Errorf("expected non-nil first review timestamp")
	}
}

func TestExtractPRComments(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/issues/6/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"created_at":"2025-03-02T00:00:00Z","user":{"login":"alice"},"body":"hi"}
		]`))
	})
	mux.HandleFunc("/repos/kmcd/foo/pulls/6/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"created_at":"2025-03-03T00:00:00Z","user":{"login":"bob"},"body":"nit","path":"foo.go"}
		]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRComments(context.Background(), connector.Repo{Slug: "kmcd/foo"}, 6, sink, &prov)
	if len(sink.comments) != 2 {
		t.Fatalf("expected 2 comments, got %d: %+v", len(sink.comments), sink.comments)
	}
	kinds := map[string]int{}
	for _, cm := range sink.comments {
		kinds[cm.Kind]++
	}
	if kinds["issue_comment"] != 1 || kinds["review_comment"] != 1 {
		t.Errorf("comment kinds = %v, want issue:1 review:1", kinds)
	}
}

func TestExtractPRReviewRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		// Single review-requested event for a user.
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"endCursor":"","hasNextPage":false},"nodes":[
			{"__typename":"ReviewRequestedEvent","createdAt":"2025-03-02T00:00:00Z","requestedReviewer":{"login":"alice"}}
		]}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRReviewRequests(context.Background(), connector.Repo{Slug: "kmcd/foo"}, 7, sink, &prov)
	if len(sink.reqs) != 1 {
		t.Fatalf("expected 1 review request row, got %d: %+v", len(sink.reqs), sink.reqs)
	}
	if sink.reqs[0].RequestedHandle != "alice" || sink.reqs[0].RequestedType != "user" {
		t.Errorf("unexpected: %+v", sink.reqs[0])
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
