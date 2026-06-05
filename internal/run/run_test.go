package run_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/run"
)

func TestRunDegenerateProducesArtifact(t *testing.T) {
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
			Raw:   "2025-01-01..2025-06-30",
		},
		Teams: map[string][]string{}, // zero repos
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "x.tar.gz")
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
	}
	artifact, err := run.Run(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if artifact == "" {
		t.Fatalf("artifact path empty")
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("artifact not present: %v", err)
	}

	entries := readTarGz(t, artifact)
	if _, ok := entries["manifest.json"]; !ok {
		t.Errorf("manifest.json missing from archive")
	}
	if _, ok := entries["metrics.sqlite"]; !ok {
		t.Errorf("metrics.sqlite missing from archive")
	}

	var m map[string]any
	if err := json.Unmarshal(entries["manifest.json"], &m); err != nil {
		t.Fatalf("manifest parse: %v", err)
	}
	if m["tool_version"] != "test" {
		t.Errorf("tool_version: %v", m["tool_version"])
	}
	if m["run_id"] == nil || m["run_id"] == "" {
		t.Errorf("run_id missing")
	}
}

func readTarGz(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		out[hdr.Name] = buf
	}
	return out
}
