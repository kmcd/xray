package manifest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kmcd/xray/internal/manifest"
)

// TestSampleManifestParses asserts that docs/sample-manifest.json (the
// customer-trust sample shipped from issue #100) round-trips cleanly into
// the current Manifest struct. A schema bump that renames or removes a
// field surfaces here so the maintainer can refresh the sample alongside
// the struct change.
func TestSampleManifestParses(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "sample-manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var m manifest.Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode sample manifest: %v", err)
	}

	if m.ToolVersion == "" {
		t.Errorf("tool_version empty")
	}
	if m.SchemaVersion == 0 {
		t.Errorf("schema_version zero")
	}
	if m.RunID == "" {
		t.Errorf("run_id empty")
	}
	if len(m.Repos) == 0 {
		t.Errorf("repos empty")
	}
	if len(m.Provenance) == 0 {
		t.Errorf("extraction_provenance empty")
	}

	// The sample is meant to demonstrate mixed-state coverage to a
	// reviewer. Guard the three demonstrative states so refreshes keep
	// the educational value.
	var sawInaccessible, sawPaginationFalse, sawError bool
	for _, p := range m.Provenance {
		for _, ep := range p.Endpoints {
			if !ep.Accessible {
				sawInaccessible = true
			}
		}
		if !p.PaginationComplete {
			sawPaginationFalse = true
		}
		if len(p.Errors) > 0 {
			sawError = true
		}
	}
	if !sawInaccessible {
		t.Errorf("sample manifest no longer demonstrates an inaccessible endpoint")
	}
	if !sawPaginationFalse {
		t.Errorf("sample manifest no longer demonstrates pagination_complete=false")
	}
	if !sawError {
		t.Errorf("sample manifest no longer demonstrates a per-row error entry")
	}
}

// TestSampleManifestCarriesNoSecrets is a coarse grep test: the sample
// artifact ships in the public repo as customer-trust evidence. A token,
// bearer header, or "authorization" line would defeat the purpose. The
// values here mirror the grep the security doc instructs reviewers to
// run against their own artifacts.
func TestSampleManifestCarriesNoSecrets(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "sample-manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	lower := strings.ToLower(string(raw))
	for _, needle := range []string{
		"bearer ", "authorization:", "x-api-key",
		"ghp_", "gho_", "ghs_", "ghu_",
		"glpat-", "xoxb-", "xoxp-",
	} {
		if strings.Contains(lower, needle) {
			t.Errorf("sample manifest contains forbidden marker %q", needle)
		}
	}
}
