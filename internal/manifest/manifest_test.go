package manifest_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/manifest"
)

func TestManifestJSONShape(t *testing.T) {
	started := time.Date(2026, 6, 4, 10, 14, 22, 0, time.UTC)
	completed := time.Date(2026, 6, 4, 10, 32, 51, 0, time.UTC)
	w := connector.Window{
		Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
	}
	m := &manifest.Manifest{
		ToolVersion:    "0.2.0",
		SchemaVersion:  1,
		RunID:          "01JTEST",
		RunStartedAt:   started,
		RunCompletedAt: completed,
		Window:         manifest.WindowJSON{Start: "2025-01-01", End: "2025-06-30"},
		Teams: map[string][]string{
			"platform": {"kmcd/foo", "kmcd/bar"},
		},
		Repos: []manifest.RepoMeta{
			{Slug: "kmcd/foo", HeadSHA: "abc", DefaultBranch: "main"},
		},
		ConnectorsUsed: []string{"github"},
		Counts:         map[string]int{"commits": 10},
		Provenance: []connector.Provenance{
			{
				Connector:          "github",
				Repo:               "kmcd/foo",
				WindowCovered:      w,
				RowsReturned:       map[string]int{"commits": 10},
				PaginationComplete: true,
				Errors:             map[string]string{},
			},
		},
	}

	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, k := range []string{
		"tool_version", "schema_version", "run_id",
		"run_started_at", "run_completed_at", "window",
		"teams", "repos", "connectors_used", "counts",
		"extraction_provenance",
	} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	win, ok := raw["window"].(map[string]any)
	if !ok {
		t.Fatalf("window not a map")
	}
	if win["start"] != "2025-01-01" || win["end"] != "2025-06-30" {
		t.Errorf("window shape: %v", win)
	}
	repos, ok := raw["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("repos shape: %v", raw["repos"])
	}
	r0 := repos[0].(map[string]any)
	for _, k := range []string{"slug", "head_sha", "default_branch"} {
		if _, ok := r0[k]; !ok {
			t.Errorf("repo missing %q", k)
		}
	}
	prov, _ := raw["extraction_provenance"].([]any)
	if len(prov) != 1 {
		t.Fatalf("provenance shape")
	}
	p0 := prov[0].(map[string]any)
	for _, k := range []string{"connector", "repo", "window_covered", "rows_returned", "pagination_complete", "rate_limit_truncated", "errors"} {
		if _, ok := p0[k]; !ok {
			t.Errorf("provenance missing %q", k)
		}
	}
}
