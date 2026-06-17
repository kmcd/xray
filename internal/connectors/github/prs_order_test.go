package github

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
)

// newTestConnectorWithOrder constructs a connector wired against the supplied
// httptest server using the given pull_request_order value.
func newTestConnectorWithOrder(t *testing.T, srv *httptest.Server, order string) *Connector {
	t.Helper()
	c, err := New(config.GitHubConn{Token: "test-token", PROrder: order}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.setBaseURL(srv.URL); err != nil {
		t.Fatalf("setBaseURL: %v", err)
	}
	return c
}

// TestFetchPRsCreatedAsc_EarlyExitAtWindowEnd verifies that fetchPRsCreatedAsc
// stops walking when a PR's createdAt exceeds window.End. The server returns
// three pages: PRs created before the window, PRs created inside the window,
// and PRs created after the window. The walk must terminate after the third
// page without requesting a fourth, and must only return in-window PRs.
func TestFetchPRsCreatedAsc_EarlyExitAtWindowEnd(t *testing.T) {
	window := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 12, 31, 23, 59, 59, 0, time.UTC),
	}

	// page1: PRs created before the window — should all be skipped (closed before window.Start)
	// or included (still open). We mix both to verify the closed-before-start filter.
	page1 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"c1","hasNextPage":true},` +
		`"nodes":[` +
		// closed before window — skip
		`{"number":1,"title":"pre-closed","body":"","createdAt":"2021-06-01T00:00:00Z","updatedAt":"2021-06-02T00:00:00Z","closedAt":"2021-11-01T00:00:00Z","author":{"login":"a"}},` +
		// closed after window.Start — include (open during window)
		`{"number":2,"title":"pre-open","body":"","createdAt":"2021-11-01T00:00:00Z","updatedAt":"2022-03-01T00:00:00Z","author":{"login":"a"}}` +
		`]}}}}}`

	// page2: PRs created inside the window — all included
	page2 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"c2","hasNextPage":true},` +
		`"nodes":[` +
		`{"number":3,"title":"in-window-1","body":"","createdAt":"2022-03-01T00:00:00Z","updatedAt":"2022-03-02T00:00:00Z","author":{"login":"a"}},` +
		`{"number":4,"title":"in-window-2","body":"","createdAt":"2022-09-01T00:00:00Z","updatedAt":"2022-09-02T00:00:00Z","author":{"login":"a"}}` +
		`]}}}}}`

	// page3: first PR is past window.End — walk terminates; second PR should never be evaluated
	page3 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"c3","hasNextPage":true},` +
		`"nodes":[` +
		`{"number":5,"title":"post-window","body":"","createdAt":"2023-01-15T00:00:00Z","updatedAt":"2023-01-16T00:00:00Z","author":{"login":"a"}},` +
		`{"number":6,"title":"way-post","body":"","createdAt":"2023-06-01T00:00:00Z","updatedAt":"2023-06-02T00:00:00Z","author":{"login":"a"}}` +
		`]}}}}}`

	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			_, _ = io.WriteString(w, page1)
		case 2:
			_, _ = io.WriteString(w, page2)
		case 3:
			_, _ = io.WriteString(w, page3)
		default:
			t.Errorf("unexpected fourth server call (walk should have stopped)")
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequests":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnectorWithOrder(t, srv, "created_asc")
	nodes, cursor, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, window, "")
	if err != nil {
		t.Fatalf("fetchPRs: %v", err)
	}
	if cursor != "" {
		t.Errorf("cursor = %q, want empty (clean stop)", cursor)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("server calls = %d, want 3 (stopped at window.End, no fourth page)", got)
	}
	// Expect PRs 2, 3, 4 — PR 1 is closed before window.Start; PRs 5 and 6 are after window.End.
	wantNums := []int{2, 3, 4}
	if len(nodes) != len(wantNums) {
		t.Fatalf("nodes = %d, want %d; numbers: %v", len(nodes), len(wantNums), prNumbers(nodes))
	}
	for i, n := range nodes {
		if got := int(n.Number); got != wantNums[i] {
			t.Errorf("nodes[%d].Number = %d, want %d", i, got, wantNums[i])
		}
	}
}

// TestFetchPRsCreatedAsc_AllInWindow verifies that when all PRs are inside
// the window the walk continues until HasNextPage is false (no spurious stop).
func TestFetchPRsCreatedAsc_AllInWindow(t *testing.T) {
	window := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 12, 31, 23, 59, 59, 0, time.UTC),
	}

	page1 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"c1","hasNextPage":true},` +
		`"nodes":[{"number":1,"title":"p1","body":"","createdAt":"2022-02-01T00:00:00Z","updatedAt":"2022-02-02T00:00:00Z","author":{"login":"a"}}]}}}}}`
	page2 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"","hasNextPage":false},` +
		`"nodes":[{"number":2,"title":"p2","body":"","createdAt":"2022-05-01T00:00:00Z","updatedAt":"2022-05-02T00:00:00Z","author":{"login":"a"}}]}}}}}`

	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			_, _ = io.WriteString(w, page1)
		} else {
			_, _ = io.WriteString(w, page2)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnectorWithOrder(t, srv, "created_asc")
	nodes, _, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, window, "")
	if err != nil {
		t.Fatalf("fetchPRs: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	if calls.Load() != 2 {
		t.Errorf("server calls = %d, want 2", calls.Load())
	}
}

// TestFetchPRsUpdatedDesc_UnchangedBehavior verifies that "updated_desc" mode
// still stops when updatedAt < window.Start, preserving the existing contract.
func TestFetchPRsUpdatedDesc_UnchangedBehavior(t *testing.T) {
	window := standardWindow() // 2025-01-01 .. 2025-12-31

	// page1: two PRs, second has updatedAt before window.Start → stopPaging
	page1 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"c1","hasNextPage":true},` +
		`"nodes":[` +
		`{"number":1,"title":"recent","body":"","createdAt":"2025-03-01T00:00:00Z","updatedAt":"2025-03-02T00:00:00Z","author":{"login":"a"}},` +
		`{"number":2,"title":"old","body":"","createdAt":"2024-06-01T00:00:00Z","updatedAt":"2024-06-02T00:00:00Z","author":{"login":"a"}}` +
		`]}}}}}`

	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, page1)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnectorWithOrder(t, srv, "updated_desc")
	nodes, _, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, window, "")
	if err != nil {
		t.Fatalf("fetchPRs: %v", err)
	}
	// PR 2 has updatedAt=2024-06-02 which is before window.Start=2025-01-01 → stopPaging
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (PR 2 triggers stopPaging)", len(nodes))
	}
	if int(nodes[0].Number) != 1 {
		t.Errorf("nodes[0].Number = %d, want 1", nodes[0].Number)
	}
	if calls.Load() != 1 {
		t.Errorf("server calls = %d, want 1 (stopped after first page)", calls.Load())
	}
}

// TestFetchPRsCreatedAsc_StaleOpenPRExcluded verifies that a PR created before
// window.Start with no closedAt but with updatedAt before window.Start (stale
// draft / abandoned PR) is excluded by the UpdatedAt guard. Without this guard
// such a PR would be collected despite having zero activity in the window.
func TestFetchPRsCreatedAsc_StaleOpenPRExcluded(t *testing.T) {
	window := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 12, 31, 23, 59, 59, 0, time.UTC),
	}

	// page1: two pre-window PRs
	//   PR 1 — stale open: created 2019, updatedAt 2019, never closed  → excluded (updatedAt < window.Start)
	//   PR 2 — active:     created 2021, updatedAt 2022 (in-window activity), never closed → included
	page1 := `{"data":{"repository":{"pullRequests":{` +
		`"pageInfo":{"endCursor":"","hasNextPage":false},` +
		`"nodes":[` +
		`{"number":1,"title":"stale-open","body":"","createdAt":"2019-06-01T00:00:00Z","updatedAt":"2019-07-01T00:00:00Z","author":{"login":"a"}},` +
		`{"number":2,"title":"active-pre","body":"","createdAt":"2021-06-01T00:00:00Z","updatedAt":"2022-02-01T00:00:00Z","author":{"login":"a"}}` +
		`]}}}}}`

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, page1)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnectorWithOrder(t, srv, "created_asc")
	nodes, _, err := c.fetchPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, window, "")
	if err != nil {
		t.Fatalf("fetchPRs: %v", err)
	}
	// Only PR 2 should be returned; PR 1 is excluded by UpdatedAt < window.Start.
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (stale PR excluded); numbers: %v", len(nodes), prNumbers(nodes))
	}
	if int(nodes[0].Number) != 2 {
		t.Errorf("nodes[0].Number = %d, want 2", nodes[0].Number)
	}
}

// prNumbers extracts PR numbers from nodes for diagnostic output.
func prNumbers(nodes []prGraph) []int {
	out := make([]int, len(nodes))
	for i, n := range nodes {
		out[i] = int(n.Number)
	}
	return out
}
