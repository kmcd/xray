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
//
// The shipped sample is a real run against goreleaser/chglog (the same
// repo /ready's smoke step uses) — clean, single-connector, no
// inaccessible endpoints, no rate-limit truncation. Failure-mode endpoint
// states are documented in docs/security.md §7 "Failure modes for
// security review" rather than reproduced here, because a healthy
// admin-scoped token against a public repo does not naturally produce
// them.
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
