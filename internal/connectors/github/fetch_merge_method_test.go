package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchMergeMethod_NoCloneFallback exercises the no-clone fallback in
// fetchMergeMethod: when the PR is merged with one parent and no clone is
// available, the function returns "squash" (preserving the historical
// parent-count heuristic).
func TestFetchMergeMethod_NoCloneFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/pulls/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":1,"merged":true,"merge_commit_sha":"mergesha"}`))
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/mergesha", func(w http.ResponseWriter, _ *http.Request) {
		// One parent.
		_, _ = w.Write([]byte(`{"sha":"mergesha","parents":[{"sha":"p1"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	got := c.fetchMergeMethod(context.Background(), "kmcd", "foo", 1, "", []string{"oid1"})
	if got != "squash" {
		t.Errorf("fetchMergeMethod no-clone fallback = %q, want squash", got)
	}
}

func TestFetchMergeMethod_TwoParentsMerge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/pulls/2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":2,"merged":true,"merge_commit_sha":"m2"}`))
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/m2", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sha":"m2","parents":[{"sha":"a"},{"sha":"b"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	got := c.fetchMergeMethod(context.Background(), "kmcd", "foo", 2, "", []string{"oid1"})
	if got != "merge" {
		t.Errorf("fetchMergeMethod two-parents = %q, want merge", got)
	}
}

func TestFetchMergeMethod_UnmergedReturnsEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/pulls/3", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":3,"merged":false}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	if got := c.fetchMergeMethod(context.Background(), "kmcd", "foo", 3, "", nil); got != "" {
		t.Errorf("fetchMergeMethod unmerged = %q, want empty", got)
	}
}

func TestFetchMergeMethod_MissingMergeSHARebase(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/pulls/4", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":4,"merged":true,"merge_commit_sha":""}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	if got := c.fetchMergeMethod(context.Background(), "kmcd", "foo", 4, "", nil); got != "rebase" {
		t.Errorf("fetchMergeMethod no merge-sha = %q, want rebase", got)
	}
}
