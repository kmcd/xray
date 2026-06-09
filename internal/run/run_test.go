package run_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	result, err := run.Run(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ArtifactPath == "" {
		t.Fatalf("artifact path empty")
	}
	if result.SHA256 == "" {
		t.Errorf("SHA256 empty in result")
	}
	if result.Size <= 0 {
		t.Errorf("Size = %d, want > 0", result.Size)
	}
	if _, err := os.Stat(result.ArtifactPath); err != nil {
		t.Fatalf("artifact not present: %v", err)
	}

	entries := readTarGz(t, result.ArtifactPath)
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

func TestRun_CancelBeforeStart_NoArtifact(t *testing.T) {
	// Pre-canceled context: Run reaches the post-clone ctx.Err() gate (no
	// repos to clone) and returns an interrupted Result with phase set,
	// no artifact written, and the temp dir cleaned up.
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
			Raw:   "2025-01-01..2025-06-30",
		},
		Teams: map[string][]string{},
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "x.tar.gz")

	var capturedTmpDir string
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		OnTempDir: func(p string) {
			capturedTmpDir = p
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := run.Run(ctx, cfg, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if !result.Interrupted {
		t.Errorf("Result.Interrupted = false, want true")
	}
	if result.InterruptedPhase != "clone" {
		t.Errorf("Result.InterruptedPhase = %q, want %q", result.InterruptedPhase, "clone")
	}
	if result.ArtifactPath != "" {
		t.Errorf("Result.ArtifactPath = %q, want empty", result.ArtifactPath)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Errorf("artifact %s exists; interrupted run should not produce one", out)
	}
	if capturedTmpDir == "" {
		t.Errorf("OnTempDir not invoked")
	}
	if _, statErr := os.Stat(capturedTmpDir); statErr == nil {
		t.Errorf("temp dir %s not cleaned up after interrupt", capturedTmpDir)
	}
	if result.TempDir != capturedTmpDir {
		t.Errorf("Result.TempDir = %q, want %q", result.TempDir, capturedTmpDir)
	}
}

func TestRun_KeepClonesOnInterrupt(t *testing.T) {
	// --keep-clones should preserve the temp dir even on graceful cancel
	// so the operator can inspect partial clones.
	cfg := &config.Config{
		Window: config.Window{
			Start: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
			Raw:   "2025-01-01..2025-06-30",
		},
		Teams: map[string][]string{},
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "x.tar.gz")

	var capturedTmpDir string
	opts := run.Options{
		Out:         out,
		KeepClones:  true,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		OnTempDir: func(p string) {
			capturedTmpDir = p
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := run.Run(ctx, cfg, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if capturedTmpDir == "" {
		t.Fatal("OnTempDir not invoked")
	}
	if _, statErr := os.Stat(capturedTmpDir); statErr != nil {
		t.Errorf("temp dir %s removed despite KeepClones=true: %v", capturedTmpDir, statErr)
	}
	// Clean up since KeepClones suppressed the auto-cleanup.
	_ = os.RemoveAll(capturedTmpDir)
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
