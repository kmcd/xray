package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
		// Review-requests follow-up: return empty timeline.
		if graphqlBodyContains(r, "REVIEW_REQUESTED_EVENT") {
			fmt.Fprintln(w, `{"data":{"repository":{"pullRequest":{"timelineItems":{"pageInfo":{"endCursor":"","hasNextPage":false},"nodes":[]}}}}}`)
			return
		}
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
