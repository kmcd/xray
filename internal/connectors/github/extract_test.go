package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kmcd/xray/internal/connector"
)

// repoSink records the InsertRepo arguments so we can assert that
// insertRepoRow filled metadata from the REST response.
type repoSink struct {
	extraSink
}

// TestInsertRepoRow drives insertRepoRow + countContributors against a
// stub. countContributors uses the rel="last" Link header to compute the
// total; we set Link manually so the connector follows the documented
// shortcut without paginating.
func TestInsertRepoRow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"language":"Go","default_branch":"main","fork":false,"archived":false,"visibility":"public","created_at":"2025-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("/repos/kmcd/foo/contributors", func(w http.ResponseWriter, r *http.Request) {
		// First page: include rel="last" pointing at page 7.
		w.Header().Set("Link", `<`+r.URL.Path+`?page=7>; rel="last"`)
		_, _ = w.Write([]byte(`[{"login":"alice"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	if err := c.insertRepoRow(context.Background(), connector.Repo{Slug: "kmcd/foo", Team: "platform"}, standardWindow(), sink, &prov); err != nil {
		t.Fatalf("insertRepoRow: %v", err)
	}
	// extraSink doesn't track InsertRepo separately; the test passes if no
	// error and provenance was incremented.
	if prov.RowsReturned["repos"] != 1 {
		t.Errorf("expected provenance.repos++; got %d", prov.RowsReturned["repos"])
	}
	if ep := prov.Endpoints["repo_metadata"]; !ep.Accessible {
		t.Errorf("expected endpoints[repo_metadata].Accessible=true on success, got %+v", ep)
	}
	if ep := prov.Endpoints["contributors"]; !ep.Accessible {
		t.Errorf("expected endpoints[contributors].Accessible=true on success, got %+v", ep)
	}
}

func TestInsertRepoRow_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	mux.HandleFunc("/repos/kmcd/foo/contributors", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	if err := c.insertRepoRow(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov); err != nil {
		t.Fatalf("insertRepoRow: %v", err)
	}
	ep, ok := prov.Endpoints["repo_metadata"]
	if !ok {
		t.Fatalf("expected endpoints[repo_metadata] entry on 403")
	}
	if ep.Accessible {
		t.Errorf("repo_metadata Accessible=false expected on 403; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("repo_metadata Reason expected on 403; got empty")
	}
	cep, ok := prov.Endpoints["contributors"]
	if !ok {
		t.Fatalf("expected endpoints[contributors] entry on 403")
	}
	if cep.Accessible {
		t.Errorf("contributors Accessible=false expected on 403; got %+v", cep)
	}
	if cep.Reason == "" {
		t.Errorf("contributors Reason expected on 403; got empty")
	}
	if prov.Errors["contributors"] == "" {
		t.Errorf("expected prov.Errors[contributors] populated on 403; got empty")
	}
}

func TestCountContributors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/contributors", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+r.URL.Path+`?page=42>; rel="last"`)
		_, _ = w.Write([]byte(`[{"login":"alice"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	n, err := c.countContributors(context.Background(), "kmcd", "foo")
	if err != nil {
		t.Fatalf("countContributors: %v", err)
	}
	if n != 42 {
		t.Errorf("countContributors = %d, want 42 (from Link rel=last)", n)
	}
}

// silence linter on unused type.
var _ = repoSink{}

// TestExtractPRs_UsesPrefetchCache verifies the #71 wall-clock optimisation:
// when Prefetch has been called for a slug, the subsequent extractPRs path
// does not issue a second GraphQL POST for the PR list. It still emits the
// PR rows from the cached nodes.
func TestExtractPRs_UsesPrefetchCache(t *testing.T) {
	mux := http.NewServeMux()
	var prListPosts int
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Distinguish the bulk PR list query from any per-PR overflow
		// queries; only the bulk shape uses pullRequests(plural).
		if graphqlBodyContains(r, "pullRequest(number:") {
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{}}}}`)
			return
		}
		prListPosts++
		fmt.Fprintln(w, buildPRListResponse([]prNodeJSON{{
			Number:    101,
			Title:     "cached PR",
			Body:      "body",
			CreatedAt: "2025-03-01T00:00:00Z",
			UpdatedAt: "2025-03-02T00:00:00Z",
			Author:    loginNode{Login: "alice"},
			Commits: commitConn{
				TotalCount: 1,
				Nodes:      []commitConnNodeWrap{{Commit: oidNode{Oid: "abc"}}},
			},
		}}))
	})
	// REST endpoints touched by emitPR (template fetch).
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)

	// Prefetch the PR side directly. The top-level Prefetch fans out to
	// both PRs and releases; this test isolates the PR cache assertion
	// and avoids needing to register a releases fake on the same mux.
	if err := c.prefetchPRs(context.Background(), "kmcd/foo", standardWindow()); err != nil {
		t.Fatalf("prefetchPRs: %v", err)
	}
	if prListPosts != 1 {
		t.Fatalf("after prefetchPRs: expected 1 PR-list POST, got %d", prListPosts)
	}

	// extractPRs should consume the cache, not refetch.
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if prListPosts != 1 {
		t.Errorf("after extractPRs with cache hit: expected POST count to stay at 1, got %d", prListPosts)
	}
	if len(sink.prs) != 1 {
		t.Errorf("expected 1 PR row emitted from cached nodes, got %d", len(sink.prs))
	}

	// A second extractPRs (cache already consumed) falls back to live fetch.
	sink2 := &extraSink{}
	prov2 := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink2, &prov2)
	if prListPosts != 2 {
		t.Errorf("after cache-miss extractPRs: expected 2 PR-list POSTs total, got %d", prListPosts)
	}
}
