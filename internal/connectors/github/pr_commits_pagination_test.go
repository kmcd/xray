package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kmcd/xray/internal/connector"
)

// TestExtractPRs_PRCommitsPagination drives a PR whose commits connection
// reports HasNextPage=true on the initial response, then HasNextPage=false
// on the continuation. The fully-paginated pr_commits row count must equal
// the PR's commit_count (101 here: 100 in the initial page + 1 in the
// continuation), exercising paginatePRCommits end-to-end.
func TestExtractPRs_PRCommitsPagination(t *testing.T) {
	const totalCommits = 101
	firstPage := make([]commitConnNodeWrap, 100)
	for i := range firstPage {
		firstPage[i] = commitConnNodeWrap{Commit: oidNode{Oid: fmt.Sprintf("sha%03d", i)}}
	}

	var gqlCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		n := gqlCalls.Add(1)
		switch n {
		case 1:
			// PR list query: include the PR with first 100 commits and
			// HasNextPage=true on the commits connection.
			nodes := []prNodeJSON{{
				Number:      77,
				Title:       "big PR",
				Body:        "body",
				CreatedAt:   "2025-03-01T00:00:00Z",
				UpdatedAt:   "2025-03-10T00:00:00Z",
				BaseRefName: "main",
				HeadRefName: "feat",
				HeadRefOid:  "deadbeef",
				Author:      loginNode{Login: "alice"},
				Commits: commitConn{
					TotalCount: totalCommits,
					PageInfo:   pageInfoNode{EndCursor: "cursor-1", HasNextPage: true},
					Nodes:      firstPage,
				},
			}}
			fmt.Fprintln(w, buildPRListResponse(nodes))
		default:
			// Continuation page: include the remaining 1 commit and
			// HasNextPage=false. The query targets pullRequest(number:N).commits(after:cursor).
			payload := map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"commits": map[string]any{
								"pageInfo": map[string]any{"endCursor": "", "hasNextPage": false},
								"nodes": []map[string]any{
									{"commit": map[string]any{"oid": "sha100"}},
								},
							},
						},
					},
				},
			}
			b, _ := json.Marshal(payload)
			_, _ = w.Write(b)
		}
	})
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/repos/kmcd/foo/pulls/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"), strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"number":77,"merged":false}`))
	})
	mux.HandleFunc("/repos/kmcd/foo/issues/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink, _ := runExtractPRs(t, c, "kmcd/foo", standardWindow())

	if len(sink.prs) != 1 {
		t.Fatalf("expected 1 PR row, got %d", len(sink.prs))
	}
	if sink.prs[0].CommitCount != totalCommits {
		t.Errorf("prs.commit_count = %d, want %d", sink.prs[0].CommitCount, totalCommits)
	}
	if got := len(sink.prCommits); got != totalCommits {
		t.Errorf("pr_commits rows = %d, want %d (initial 100 + 1 continuation)", got, totalCommits)
	}
}

// TestPaginatePRCommits_ReturnsOids verifies that paginatePRCommits returns
// every OID it collects from the overflow pages. This is the property that
// enables resolveMergeMethod to see the full head-commit set for PRs with
// more than 25 commits.
func TestPaginatePRCommits_ReturnsOids(t *testing.T) {
	const (
		oid1 = "overflow-sha-001"
		oid2 = "overflow-sha-002"
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		payload := map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					"pullRequest": map[string]any{
						"commits": map[string]any{
							"pageInfo": map[string]any{"endCursor": "", "hasNextPage": false},
							"nodes": []map[string]any{
								{"commit": map[string]any{"oid": oid1}},
								{"commit": map[string]any{"oid": oid2}},
							},
						},
					},
				},
			},
		}
		b, _ := json.Marshal(payload)
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	b := openPRCommitsBatch(sink)
	oids := c.paginatePRCommits(context.Background(), "kmcd", "foo", 77, "kmcd/foo", "cursor-start", b, &prov)
	commitBatch(b, &prov, "pr_commits")

	if len(oids) != 2 {
		t.Fatalf("paginatePRCommits returned %d OIDs, want 2: %v", len(oids), oids)
	}
	if oids[0] != oid1 || oids[1] != oid2 {
		t.Errorf("paginatePRCommits OIDs = %v, want [%s %s]", oids, oid1, oid2)
	}
	if len(sink.prCommits) != 2 {
		t.Errorf("pr_commits rows = %d, want 2", len(sink.prCommits))
	}
}
