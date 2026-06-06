package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchSignatureVerified(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/commits/abc", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"commit":{"verification":{"verified":true}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	v := c.fetchSignatureVerified(context.Background(), "kmcd", "foo", "abc")
	if v == nil || *v != true {
		t.Errorf("expected *bool true, got %v", v)
	}
}

func TestFetchSignatureVerifiedMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/commits/abc", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	if v := c.fetchSignatureVerified(context.Background(), "kmcd", "foo", "abc"); v != nil {
		t.Errorf("expected nil, got %v", *v)
	}
}

func TestFetchLandedViaPR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/commits/abc/pulls", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `[{"number":1}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)
	cache := map[string]*bool{}
	v := c.fetchLandedViaPR(context.Background(), "kmcd", "foo", "abc", cache)
	if v == nil || *v != true {
		t.Errorf("expected landed-via-pr true, got %v", v)
	}
	// Second call should hit cache (same address back).
	v2 := c.fetchLandedViaPR(context.Background(), "kmcd", "foo", "abc", cache)
	if v2 != v {
		t.Errorf("expected cached *bool, got fresh value")
	}
}
