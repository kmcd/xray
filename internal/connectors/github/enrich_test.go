package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEnrichCommits_HappyPath exercises a single batch: three commits, all
// with both signals available, no errors. Asserts the request shape and
// the decoded result.
func TestEnrichCommits_HappyPath(t *testing.T) {
	shas := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"1123456789abcdef0123456789abcdef01234568",
		"2123456789abcdef0123456789abcdef01234569",
	}

	mux := http.NewServeMux()
	var lastBody string
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		// Respond with a payload aliased a0/a1/a2 matching the request shape.
		_, _ = w.Write([]byte(`{"data":{"repository":{
			"a0":{"signature":{"isValid":true}},
			"a1":{"signature":{"isValid":false}},
			"a2":{"signature":null}
		}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	got, err := c.enrichCommits(context.Background(), "kmcd", "foo", shas)
	if err != nil {
		t.Fatalf("enrichCommits: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d enrichments, want 3", len(got))
	}

	// Request shape: one POST whose JSON-encoded query contains every SHA
	// as an alias plus the two fields we care about. Body is JSON-encoded
	// so embedded quotes appear escaped.
	for i, sha := range shas {
		if !strings.Contains(lastBody, sha) {
			t.Errorf("request body missing SHA %s for alias a%d", sha, i)
		}
	}
	if !strings.Contains(lastBody, "signature") {
		t.Errorf("request body missing signature field")
	}
	if strings.Contains(lastBody, "associatedPullRequests") {
		t.Errorf("request body still contains associatedPullRequests; #75 trimmed it (landed_via_pr now derived in postprocess)")
	}
	if !strings.Contains(lastBody, "a0") || !strings.Contains(lastBody, "a2") {
		t.Errorf("request body missing alias labels (a0, a2)")
	}

	// a0: signed -> SignatureVerified *true.
	a0 := got[shas[0]]
	if a0.SignatureVerified == nil || !*a0.SignatureVerified {
		t.Errorf("a0 SignatureVerified want *true, got %v", a0.SignatureVerified)
	}
	// a1: not signed -> SignatureVerified *false.
	a1 := got[shas[1]]
	if a1.SignatureVerified == nil || *a1.SignatureVerified {
		t.Errorf("a1 SignatureVerified want *false, got %v", a1.SignatureVerified)
	}
	// a2: signature null -> SignatureVerified nil.
	a2 := got[shas[2]]
	if a2.SignatureVerified != nil {
		t.Errorf("a2 SignatureVerified want nil, got %v", *a2.SignatureVerified)
	}
}

// TestEnrichCommits_Batching exercises the batch-size split. POST count
// should equal ceil(len / enrichBatchSize); we compute the expected value
// rather than hard-code it so changing the constant doesn't break the
// test.
func TestEnrichCommits_Batching(t *testing.T) {
	const n = 105
	shas := make([]string, n)
	for i := range shas {
		shas[i] = "abcdef0123456789abcdef0123456789abcdef01"
	}

	mux := http.NewServeMux()
	var posts int
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		posts++
		_, _ = w.Write([]byte(`{"data":{"repository":{}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	if _, err := c.enrichCommits(context.Background(), "kmcd", "foo", shas); err != nil {
		t.Fatalf("enrichCommits: %v", err)
	}
	want := (n + enrichBatchSize - 1) / enrichBatchSize
	if posts != want {
		t.Errorf("graphql POSTs = %d, want %d (%d / batch size %d)", posts, want, n, enrichBatchSize)
	}
}

// TestEnrichCommits_SkipsNonHexSHAs guards against accidentally injecting a
// non-hex string into the GraphQL query (which would be a parse error or
// worse, an injection vector).
func TestEnrichCommits_SkipsNonHexSHAs(t *testing.T) {
	mux := http.NewServeMux()
	var got string
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var payload struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(b, &payload)
		got = payload.Query
		_, _ = w.Write([]byte(`{"data":{"repository":{}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	_, _ = c.enrichCommits(context.Background(), "kmcd", "foo", []string{
		"0123456789abcdef0123456789abcdef01234567",
		"not-a-sha",
		"\"; drop everything",
	})
	if strings.Contains(got, "not-a-sha") {
		t.Errorf("query contains non-hex SHA: %q", got)
	}
	if strings.Contains(got, "drop everything") {
		t.Errorf("query contains injected fragment: %q", got)
	}
}

// TestEnrichCommits_PartialErrors covers GraphQL's "data plus errors"
// response shape: some aliases succeed, the server reports errors for
// others. We expect successful aliases to be returned and errors logged.
func TestEnrichCommits_PartialErrors(t *testing.T) {
	shas := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"1123456789abcdef0123456789abcdef01234568",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"data": {"repository": {
				"a0": {"signature":{"isValid":true}}
			}},
			"errors": [{"message":"a1 unavailable"}]
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	got, err := c.enrichCommits(context.Background(), "kmcd", "foo", shas)
	if err != nil {
		t.Fatalf("enrichCommits: %v", err)
	}
	if _, ok := got[shas[0]]; !ok {
		t.Errorf("a0 enrichment missing despite successful alias")
	}
	if _, ok := got[shas[1]]; ok {
		t.Errorf("a1 enrichment present despite error")
	}
}

// TestEnrichCommits_HTTPFailureContinues confirms that an HTTP-level
// failure on a single batch is logged but doesn't abort other batches.
func TestEnrichCommits_HTTPFailureContinues(t *testing.T) {
	shas := make([]string, 150)
	for i := range shas {
		shas[i] = "abcdef0123456789abcdef0123456789abcdef01"
	}

	mux := http.NewServeMux()
	var calls int
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	// Override the policy budget for the test so retries don't blow the
	// timeout — the ratelimit transport retries 429s up to 3 attempts +
	// 60 s. By the time enrichCommits returns the first batch has retried,
	// failed, moved on; the second batch should still go.
	_, err := c.enrichCommits(context.Background(), "kmcd", "foo", shas)
	if err != nil {
		t.Fatalf("enrichCommits returned %v; expected nil (batch failures swallowed)", err)
	}
	if calls < 2 {
		t.Errorf("expected at least 2 batch attempts, got %d", calls)
	}
}
