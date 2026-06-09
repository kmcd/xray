package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmcd/xray/internal/connector"
)

// TestFetchPRs_EOFRetryThenSuccess simulates a mid-response truncation on
// the first GraphQL page (server hijacks the connection, writes a
// Content-Length far larger than the body it actually flushes, then closes)
// followed by a clean response on the retry. fetchPRs must complete with
// the full node set, exercising both the costInterceptor error-propagation
// fix and queryWithEOFRetry.
func TestFetchPRs_EOFRetryThenSuccess(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			truncateMidBody(t, w)
			return
		}
		fmt.Fprintln(w, buildPRListResponse([]prNodeJSON{{
			Number:    42,
			Title:     "ok",
			Body:      "body",
			CreatedAt: "2025-03-01T00:00:00Z",
			UpdatedAt: "2025-03-02T00:00:00Z",
			Author:    loginNode{Login: "alice"},
		}}))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	nodes, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow())
	if err != nil {
		t.Fatalf("fetchPRs: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server calls = %d, want 2 (one EOF + one retry)", got)
	}
}

// TestFetchPRs_EOFRetryExhausted simulates a clean first page (so partial
// nodes accumulate) followed by every subsequent attempt EOFing. fetchPRs
// must return the first page's nodes plus the terminal EOF error so
// extractPRs can still emit what it has.
func TestFetchPRs_EOFRetryExhausted(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			// First page: one PR, HasNextPage=true so the walk continues.
			payload := `{"data":{"repository":{"pullRequests":{` +
				`"pageInfo":{"endCursor":"c1","hasNextPage":true},` +
				`"nodes":[{"number":1,"title":"first","body":"b","createdAt":"2025-03-01T00:00:00Z","updatedAt":"2025-03-02T00:00:00Z","author":{"login":"a"}}]}}}}`
			_, _ = io.WriteString(w, payload)
			return
		}
		truncateMidBody(t, w)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	nodes, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow())
	if err == nil {
		t.Fatalf("fetchPRs: expected error, got nil")
	}
	if !isTransientEOF(err) {
		t.Errorf("error is not transient EOF: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("nodes = %d, want 1 (first page survives)", len(nodes))
	}
	// First success + 3 retry attempts on second page.
	if got := calls.Load(); got != 4 {
		t.Errorf("server calls = %d, want 4 (page 1 + 3 attempts on page 2)", got)
	}
}

// TestCostInterceptor_PropagatesReadError verifies costInterceptor returns
// (nil, err) when the body read fails. This is the layer that previously
// swallowed the read error and re-attached the partial body, causing a
// downstream JSON decoder "unexpected EOF" that nothing retried.
func TestCostInterceptor_PropagatesReadError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		truncateMidBody(t, w)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ci := &costInterceptor{
		base:   http.DefaultTransport,
		onCost: func(int, int) {},
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/graphql", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ci.RoundTrip(req)
	if err == nil {
		t.Fatalf("RoundTrip: expected error on truncated body, got nil (resp=%v)", resp)
	}
	if resp != nil {
		t.Errorf("RoundTrip: expected nil response on error, got %+v", resp)
	}
	if !isTransientEOF(err) {
		t.Errorf("error is not transient EOF: %v", err)
	}
}

// truncateMidBody hijacks the connection, writes headers declaring a large
// Content-Length, flushes only a few bytes, then closes the connection.
// The client's io.ReadAll returns io.ErrUnexpectedEOF.
func truncateMidBody(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("ResponseWriter is not a Hijacker")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 4096\r\nContent-Type: application/json\r\n\r\n")
	_, _ = io.WriteString(conn, `{"data":{`)
}

