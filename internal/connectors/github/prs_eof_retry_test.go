package github

import (
	"context"
	"errors"
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
	nodes, _, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), "")
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
	nodes, _, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), "")
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
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/graphql", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ci.RoundTrip(req)
	if resp != nil {
		defer resp.Body.Close()
	}
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

// TestExtractPRs_CursorHandoff exercises the prefetch-failed-mid-walk →
// extractPRs-resumes-live path. Sequence: page 1 succeeds via Prefetch,
// page 2 truncates 3× (exhausting queryWithEOFRetry inside Prefetch),
// extractPRs then live-fetches page 2 from the stashed cursor and the
// server returns a clean page 2 + page 3 marker. The full PR set must
// reach the sink even though Prefetch gave up.
func TestExtractPRs_CursorHandoff(t *testing.T) {
	var calls atomic.Int32
	page1JSON := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"c1","hasNextPage":true},` +
		`"nodes":[{"number":1,"title":"p1","body":"b","createdAt":"2025-03-01T00:00:00Z","updatedAt":"2025-03-02T00:00:00Z","author":{"login":"a"}}]}}}}`
	page2JSON := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"","hasNextPage":false},` +
		`"nodes":[{"number":2,"title":"p2","body":"b","createdAt":"2025-03-03T00:00:00Z","updatedAt":"2025-03-04T00:00:00Z","author":{"login":"a"}}]}}}}`

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch {
		case n == 1:
			// Prefetch page 1: clean.
			_, _ = io.WriteString(w, page1JSON)
		case n >= 2 && n <= 4:
			// Prefetch page 2 attempts (3× retries inside queryWithEOFRetry).
			truncateMidBody(t, w)
		case n == 5:
			// extractPRs resume: must include cursor "c1" in body.
			if !graphqlBodyContains(r, "c1") {
				t.Errorf("resume request missing cursor c1; body did not contain it")
			}
			_, _ = io.WriteString(w, page2JSON)
		default:
			// REST follow-up calls from emitPRs (template, /pulls/N, etc.).
			emptyJSONOK(w, r)
		}
	})
	// REST endpoints reachable from emitPR.
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/repos/kmcd/foo/pulls/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"), strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"number":1,"merged":false}`))
	})
	mux.HandleFunc("/repos/kmcd/foo/issues/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	if err := c.Prefetch(context.Background(), "kmcd/foo", standardWindow()); err == nil {
		t.Fatalf("Prefetch: expected EOF error after retry budget; got nil")
	}
	sink, prov := runExtractPRs(t, c, "kmcd/foo", standardWindow())

	if len(sink.prs) != 2 {
		t.Fatalf("sink.prs = %d, want 2 (page 1 from prefetch + page 2 from resume)", len(sink.prs))
	}
	if _, recorded := prov.Errors["prs"]; recorded {
		t.Errorf("prov.Errors[prs] should be empty after successful resume; got %q", prov.Errors["prs"])
	}
}

// TestPaginatePRReviewsOverflow_EOFRetry verifies that the overflow review
// pagination path retries on transient EOF (it shares the queryWithEOFRetry
// path with fetchPRs after the #80 follow-up).
func TestPaginatePRReviewsOverflow_EOFRetry(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			truncateMidBody(t, w)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"reviews":{`+
			`"pageInfo":{"endCursor":"","hasNextPage":false},`+
			`"nodes":[{"state":"APPROVED","submittedAt":"2025-03-05T00:00:00Z","body":"lgtm","author":{"login":"r"}}]}}}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	b := openReviewsBatch(sink)
	got := c.paginatePRReviewsOverflow(context.Background(), "kmcd", "foo", 1, "kmcd/foo", "c1", b, &prov)
	commitBatch(b, &prov, "reviews")
	if got == nil {
		t.Fatalf("expected non-nil first-submitted time after successful retry")
	}
	if calls.Load() != 2 {
		t.Errorf("server calls = %d, want 2 (one EOF + one retry)", calls.Load())
	}
	if v, ok := prov.Errors["reviews"]; ok && v != "" {
		t.Errorf("prov.Errors[reviews] should be empty after retry succeeded; got %q", v)
	}
}

// TestIsTransientEOF_ConnReset verifies that connection reset by peer errors
// (surfaced after long idle periods when GitHub closes pooled connections
// server-side) are classified as transient so queryWithEOFRetry retries.
func TestIsTransientEOF_ConnReset(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"io.EOF", io.EOF, true},
		{"unexpected EOF sentinel", io.ErrUnexpectedEOF, true},
		{"unexpected EOF string", errors.New("unexpected EOF"), true},
		{"connection reset by peer", errors.New(`Post "https://api.github.com/graphql": read tcp 1.2.3.4:56789->140.82.113.22:443: read: connection reset by peer`), true},
		{"Connection reset mixed case", errors.New("Connection reset by peer"), true},
		{"other error", errors.New("server returned HTTP 500"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientEOF(tc.err); got != tc.want {
				t.Errorf("isTransientEOF(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsStreamCancel verifies the classification of HTTP/2 stream CANCEL errors.
func TestIsStreamCancel(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canonical CANCEL", errors.New(`Post "https://api.github.com/graphql": http2: stream error: stream ID 135; CANCEL; received from peer`), true},
		{"CANCEL mixed case", errors.New(`Post "https://api.github.com/graphql": HTTP2: Stream Error: Stream ID 7; CANCEL; Received From Peer`), true},
		{"context cancelled", errors.New("context canceled"), false},
		{"io.EOF", io.EOF, false},
		{"connection reset", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStreamCancel(tc.err); got != tc.want {
				t.Errorf("isStreamCancel(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
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
