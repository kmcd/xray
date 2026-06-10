package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzParseWindow asserts parseWindow never panics on arbitrary input and
// that any returned Window has both bounds non-zero. parseWindow is the only
// hand-rolled parser in config; everything else goes through BurntSushi/toml.
func FuzzParseWindow(f *testing.F) {
	for _, s := range []string{
		"2025-01-01..2025-06-30",
		"2025-12-31..2026-01-01",
		"",
		"..",
		"2025-01-01",
		"not-a-date..2025-01-01",
		"2025-13-40..2025-01-01",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		w, err := parseWindow(s)
		if err != nil {
			return
		}
		if w.Start.IsZero() || w.End.IsZero() {
			t.Fatalf("parseWindow(%q) returned no error but zero bound: %+v", s, w)
		}
		if w.Raw != s {
			t.Fatalf("parseWindow(%q): Raw=%q, want input verbatim", s, w.Raw)
		}
	})
}

// FuzzLoad asserts Load never panics on arbitrary TOML bytes. The file is
// written to a temp path because Load reads from disk.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(`window = "2025-01-01..2025-06-30"`))
	f.Add([]byte(`[connectors.github]
token = "x"`))
	f.Add([]byte(``))
	f.Add([]byte(`window = "garbage"`))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "xray.toml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write seed: %v", err)
		}
		cfg, meta, err := Load(path)
		if err != nil {
			return
		}
		if cfg == nil || meta == nil {
			t.Fatalf("Load returned nil cfg=%v meta=%v with no error", cfg, meta)
		}
		// Validate must not panic on whatever Load accepted.
		_ = Validate(cfg, meta, path)
	})
}
