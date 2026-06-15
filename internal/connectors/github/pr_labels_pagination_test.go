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

// TestExtractPRs_PRLabelsPagination drives a PR whose labels connection
// reports HasNextPage=true on the initial response, then HasNextPage=false
// on the continuation. The fully-paginated pr_labels row count must equal
// the total label count (11 here: 10 in the initial inline page + 1 in the
// continuation), exercising paginatePRLabelsOverflow end-to-end.
func TestExtractPRs_PRLabelsPagination(t *testing.T) {
	const totalLabels = 11
	firstPage := make([]labelNode, 10)
	for i := range firstPage {
		firstPage[i] = labelNode{Name: fmt.Sprintf("label-%02d", i)}
	}

	var gqlCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		n := gqlCalls.Add(1)
		switch n {
		case 1:
			nodes := []prNodeJSON{{
				Number:      88,
				Title:       "labelled PR",
				Body:        "body",
				CreatedAt:   "2025-03-01T00:00:00Z",
				UpdatedAt:   "2025-03-10T00:00:00Z",
				BaseRefName: "main",
				HeadRefName: "feat",
				HeadRefOid:  "deadbeef",
				Author:      loginNode{Login: "alice"},
				Labels: labelConn{
					PageInfo: pageInfoNode{EndCursor: "cursor-1", HasNextPage: true},
					Nodes:    firstPage,
				},
			}}
			fmt.Fprintln(w, buildPRListResponse(nodes))
		default:
			payload := map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"labels": map[string]any{
								"pageInfo": map[string]any{"endCursor": "", "hasNextPage": false},
								"nodes": []map[string]any{
									{"name": "label-10"},
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
		_, _ = w.Write([]byte(`{"number":88,"merged":false}`))
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
	if got := len(sink.prLabels); got != totalLabels {
		t.Errorf("pr_labels rows = %d, want %d (initial 10 + 1 continuation)", got, totalLabels)
	}
}
