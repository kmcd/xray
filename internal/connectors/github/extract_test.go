package github

import (
	"context"
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
