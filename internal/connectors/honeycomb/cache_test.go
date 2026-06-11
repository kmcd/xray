package honeycomb

import (
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCacheFingerprintStable(t *testing.T) {
	a := cacheFingerprint("tok", "ds", "https://api.honeycomb.io/1")
	b := cacheFingerprint("tok", "ds", "https://api.honeycomb.io/1")
	if a != b {
		t.Errorf("fingerprint not stable: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("fingerprint length = %d, want 16", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("fingerprint not valid hex: %v", err)
	}
	// Different inputs produce different fingerprints.
	c := cacheFingerprint("other-tok", "ds", "https://api.honeycomb.io/1")
	if a == c {
		t.Errorf("different tokens produced same fingerprint")
	}
}

func TestCacheFingerprintTokenNotExposed(t *testing.T) {
	token := "supersecrettoken12345"
	fp := cacheFingerprint(token, "ds", "https://api.honeycomb.io/1")
	if strings.Contains(fp, token) {
		t.Errorf("fingerprint contains the raw token")
	}
}

func TestCachePathStructure(t *testing.T) {
	path, err := cachePath("abc123")
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path %q is not absolute", path)
	}
	if !strings.Contains(path, filepath.Join("xray", "honeycomb")) {
		t.Errorf("path %q does not contain xray/honeycomb sub-path", path)
	}
	if !strings.HasSuffix(path, "abc123.json") {
		t.Errorf("path %q does not end with fingerprint.json", path)
	}
}

func TestReadWriteMarkerCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	markers := []marker{
		{ID: "m1", Message: "v1", Type: "prod", StartTime: 1000},
		{ID: "m2", Message: "v2", Type: "staging", StartTime: 2000},
	}
	writeMarkerCache(path, markers, slog.Default())

	got, fresh := readMarkerCache(path)
	if !fresh {
		t.Errorf("expected fresh=true for just-written cache")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 markers, got %d", len(got))
	}
	if got[0].ID != "m1" || got[1].ID != "m2" {
		t.Errorf("marker IDs mismatch: got %v", got)
	}
}

func TestReadMarkerCacheExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	env := cacheEnvelope{
		Version:   cacheSchemaVersion,
		FetchedAt: time.Now().UTC().Add(-(cacheTTL + time.Hour)),
		Markers:   []marker{{ID: "old"}},
	}
	data, _ := json.Marshal(env)
	_ = os.WriteFile(path, data, 0o600)

	_, fresh := readMarkerCache(path)
	if fresh {
		t.Errorf("expected fresh=false for expired cache")
	}
}

func TestReadMarkerCacheCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	_ = os.WriteFile(path, []byte("not json at all {{{"), 0o600)

	got, fresh := readMarkerCache(path)
	if got != nil || fresh {
		t.Errorf("expected (nil, false) for corrupt cache; got (%v, %v)", got, fresh)
	}
}

func TestReadMarkerCacheVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	env := cacheEnvelope{
		Version:   99,
		FetchedAt: time.Now().UTC(),
		Markers:   []marker{{ID: "x"}},
	}
	data, _ := json.Marshal(env)
	_ = os.WriteFile(path, data, 0o600)

	_, fresh := readMarkerCache(path)
	if fresh {
		t.Errorf("expected fresh=false for version mismatch")
	}
}

func TestWriteMarkerCacheAtomicNoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	writeMarkerCache(path, []marker{{ID: "a"}}, slog.Default())

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("tmp file left behind: %s", e.Name())
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode = %o, want 0o600", info.Mode().Perm())
	}
}

func TestReadMarkerCacheMissingFile(t *testing.T) {
	got, fresh := readMarkerCache("/does/not/exist/cache.json")
	if got != nil || fresh {
		t.Errorf("expected (nil, false) for missing file; got (%v, %v)", got, fresh)
	}
}
