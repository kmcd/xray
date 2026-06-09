package github

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kmcd/xray/internal/connector"
)

func TestParseTemplate(t *testing.T) {
	tpl := parseTemplate("## Summary\n\nSome blurb\n\n## Test plan\n\n## Risks\n")
	if tpl == nil {
		t.Fatal("expected template, got nil")
	}
	if len(tpl.headings) != 3 {
		t.Fatalf("expected 3 headings, got %d: %v", len(tpl.headings), tpl.headings)
	}
}

func TestParseTemplateNoHeadings(t *testing.T) {
	if tpl := parseTemplate("just prose with no headings\n"); tpl != nil {
		t.Errorf("expected nil for header-less template, got %+v", tpl)
	}
}

func TestTemplateScore(t *testing.T) {
	tpl := parseTemplate("## Summary\n## Test plan\n## Risks\n")
	body := "## Summary\n\nfoo\n\n## Test plan\n\nbar\n"
	got := tpl.score(body)
	want := 2.0 / 3.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("score = %v, want %v", got, want)
	}
}

func TestTemplateScoreAllPresent(t *testing.T) {
	tpl := parseTemplate("## A\n## B\n")
	if got := tpl.score("the a part\nand the b part\n"); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("score = %v, want 1", got)
	}
}

func TestTemplateScoreNilTemplate(t *testing.T) {
	var tpl *template
	if got := tpl.score("anything"); got != 0 {
		t.Errorf("nil template score = %v, want 0", got)
	}
}

// TestFetchTemplate_NotFound covers the path where the .github/PULL_REQUEST_TEMPLATE.md
// file is absent (404 from GetContents). The endpoint is still reachable so
// Endpoints["pr_template"].Accessible must be true; the call returns (nil, nil).
func TestFetchTemplate_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	tpl, err := c.fetchTemplate(context.Background(), "kmcd/foo", &prov)
	if err != nil {
		t.Fatalf("fetchTemplate: %v", err)
	}
	if tpl != nil {
		t.Errorf("expected nil template on 404, got %+v", tpl)
	}
	if ep := prov.Endpoints["pr_template"]; !ep.Accessible {
		t.Errorf("expected Accessible=true on 404 (endpoint reachable), got %+v", ep)
	}
}

// TestFetchTemplate_Forbidden covers the 403 path: token lacks permission to
// read repo contents. Endpoints["pr_template"].Accessible is false; the err
// is swallowed so the per-PR loop doesn't log it for every PR.
func TestFetchTemplate_Forbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	tpl, err := c.fetchTemplate(context.Background(), "kmcd/foo", &prov)
	if err != nil {
		t.Errorf("expected err swallowed on 403, got %v", err)
	}
	if tpl != nil {
		t.Errorf("expected nil template on 403, got %+v", tpl)
	}
	ep, ok := prov.Endpoints["pr_template"]
	if !ok {
		t.Fatalf("expected endpoints[pr_template] entry on 403")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on 403, got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on 403, got empty")
	}
}

// TestFetchTemplate_ServerError covers the 5xx / network err path: Endpoints
// is written Accessible:false and the err is bubbled to the caller so its
// existing warn log fires once per Extract.
func TestFetchTemplate_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	tpl, err := c.fetchTemplate(context.Background(), "kmcd/foo", &prov)
	if err == nil {
		t.Errorf("expected err bubbled on 500, got nil")
	}
	if tpl != nil {
		t.Errorf("expected nil template on 500, got %+v", tpl)
	}
	ep, ok := prov.Endpoints["pr_template"]
	if !ok {
		t.Fatalf("expected endpoints[pr_template] entry on 500")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on 500, got %+v", ep)
	}
}

// TestFetchTemplate_Success covers the happy path: 200 with a templated body.
// Endpoints["pr_template"].Accessible=true and the parsed template is returned.
func TestFetchTemplate_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		// GitHub returns the file body base64-encoded in a "content" field.
		// "## Summary\n## Test plan\n" -> base64:
		_, _ = w.Write([]byte(`{"name":"PULL_REQUEST_TEMPLATE.md","encoding":"base64","content":"IyMgU3VtbWFyeQojIyBUZXN0IHBsYW4K"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	tpl, err := c.fetchTemplate(context.Background(), "kmcd/foo", &prov)
	if err != nil {
		t.Fatalf("fetchTemplate: %v", err)
	}
	if tpl == nil || len(tpl.headings) != 2 {
		t.Errorf("expected parsed template with 2 headings, got %+v", tpl)
	}
	if ep := prov.Endpoints["pr_template"]; !ep.Accessible {
		t.Errorf("expected Accessible=true on success, got %+v", ep)
	}
}
