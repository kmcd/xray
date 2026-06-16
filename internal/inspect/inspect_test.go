package inspect

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/manifest"
	"github.com/kmcd/xray/internal/model"
	_ "modernc.org/sqlite"
)

// buildArtifact synthesises a minimal .tar.gz containing manifest.json and
// metrics.sqlite. The manifest counts reflect the actual DB state.
func buildArtifact(t *testing.T, dir string, mfst *manifest.Manifest, dbPath string) string {
	t.Helper()
	mfstBytes, err := json.MarshalIndent(mfst, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	artPath := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(artPath)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	writeEntry := func(name string, data []byte) {
		t.Helper()
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o644,
			Size:    int64(len(data)),
			ModTime: time.Unix(0, 0).UTC(),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write tar body %s: %v", name, err)
		}
	}

	writeEntry("manifest.json", mfstBytes)

	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	writeEntry("metrics.sqlite", dbBytes)

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return artPath
}

// buildDB creates a minimal SQLite database with the canonical DDL applied,
// a _schema row, and the specified tables populated. Returns the DB path.
func buildDB(t *testing.T, dir, toolVersion string, schemaVersion int) string {
	t.Helper()
	dbPath := filepath.Join(dir, "metrics.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, model.DDL); err != nil {
		t.Fatalf("apply DDL: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO _schema (schema_version, tool_version, applied_at) VALUES (?, ?, ?)`,
		schemaVersion, toolVersion, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert _schema: %v", err)
	}
	return dbPath
}

// buildManifest returns a minimal valid manifest.
func buildManifest(schemaVersion int, counts map[string]int) *manifest.Manifest {
	return &manifest.Manifest{
		ToolVersion:   "0.4.8",
		SchemaVersion: schemaVersion,
		RunID:         "01J000000000000000000000",
		Counts:        counts,
	}
}

// TestInspect_HappyPath verifies all five checks pass on a well-formed artifact.
func TestInspect_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := buildDB(t, dir, "0.4.8", model.SchemaVersion)
	mfst := buildManifest(model.SchemaVersion, map[string]int{"repos": 0, "commits": 0})
	artPath := buildArtifact(t, dir, mfst, dbPath)

	r, err := Inspect(context.Background(), artPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if !r.OK {
		for _, c := range r.Checks {
			if !c.Pass {
				t.Errorf("check %s failed: %s", c.Name, c.Detail)
			}
		}
		t.Fatalf("expected all checks to pass")
	}
	if len(r.Checks) != 5 {
		t.Errorf("expected 5 checks, got %d", len(r.Checks))
	}
	names := []string{"tar_integrity", "manifest_shape", "sqlite_integrity", "row_counts", "schema_version"}
	for i, name := range names {
		if r.Checks[i].Name != name {
			t.Errorf("check[%d]: want %q, got %q", i, name, r.Checks[i].Name)
		}
	}
}

// TestInspect_CorruptTar verifies that a truncated .tar.gz fails tar_integrity
// and that later checks are surfaced as skipped (not panicked).
func TestInspect_CorruptTar(t *testing.T) {
	dir := t.TempDir()
	dbPath := buildDB(t, dir, "0.4.8", model.SchemaVersion)
	mfst := buildManifest(model.SchemaVersion, nil)
	artPath := buildArtifact(t, dir, mfst, dbPath)

	// Truncate the archive to corrupt it.
	fi, err := os.Stat(artPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(artPath, fi.Size()/2); err != nil {
		t.Fatal(err)
	}

	r, err := Inspect(context.Background(), artPath)
	if err != nil {
		t.Fatalf("Inspect returned unexpected I/O error: %v", err)
	}
	if r.OK {
		t.Fatal("expected OK=false on corrupt archive")
	}
	if r.Checks[0].Pass {
		t.Error("tar_integrity check should fail on corrupt archive")
	}
	// Later checks must still be emitted (shape stable).
	if len(r.Checks) != 5 {
		t.Errorf("expected 5 checks even on failure, got %d", len(r.Checks))
	}
	for i := 1; i < len(r.Checks); i++ {
		if r.Checks[i].Pass {
			t.Errorf("check[%d] %s should not pass when tar_integrity failed", i, r.Checks[i].Name)
		}
	}
}

// TestInspect_MissingManifestField verifies check (b) fails when ToolVersion
// is empty and names the missing field.
func TestInspect_MissingManifestField(t *testing.T) {
	dir := t.TempDir()
	dbPath := buildDB(t, dir, "0.4.8", model.SchemaVersion)
	mfst := buildManifest(model.SchemaVersion, nil)
	mfst.ToolVersion = "" // zero out required field
	artPath := buildArtifact(t, dir, mfst, dbPath)

	r, err := Inspect(context.Background(), artPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if r.Checks[0].Name != "tar_integrity" || !r.Checks[0].Pass {
		t.Errorf("tar_integrity should pass; got pass=%v detail=%q", r.Checks[0].Pass, r.Checks[0].Detail)
	}
	manifestCheck := r.Checks[1]
	if manifestCheck.Pass {
		t.Error("manifest_shape should fail when tool_version is empty")
	}
	if manifestCheck.Detail == "" {
		t.Error("manifest_shape detail should name the missing field")
	}
}

// TestInspect_RowCountMismatch verifies check (d) catches when the DB has
// more rows than the manifest claims.
func TestInspect_RowCountMismatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := buildDB(t, dir, "0.4.8", model.SchemaVersion)

	// Insert one extra repo row into the DB.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(context.Background(), `INSERT INTO repos (slug, default_branch, head_sha, team) VALUES ('org/repo', 'main', 'abc123', 'eng')`)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	db.Close()

	// Manifest claims 0 repos, but DB has 1.
	mfst := buildManifest(model.SchemaVersion, map[string]int{"repos": 0})
	artPath := buildArtifact(t, dir, mfst, dbPath)

	r, err := Inspect(context.Background(), artPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	rowCheck := r.Checks[3]
	if rowCheck.Name != "row_counts" {
		t.Fatalf("expected check[3]=row_counts, got %q", rowCheck.Name)
	}
	if rowCheck.Pass {
		t.Error("row_counts should fail on mismatch")
	}
	if len(rowCheck.Mismatches) == 0 {
		t.Error("expected Mismatches to be populated")
	}
	if rowCheck.Mismatches[0].Table != "repos" {
		t.Errorf("expected mismatch on table=repos, got %q", rowCheck.Mismatches[0].Table)
	}
	if rowCheck.Mismatches[0].Manifest != 0 || rowCheck.Mismatches[0].DB != 1 {
		t.Errorf("mismatch values: manifest=%d db=%d", rowCheck.Mismatches[0].Manifest, rowCheck.Mismatches[0].DB)
	}
}

// TestInspect_SchemaVersionMismatch verifies check (e) fails when _schema
// has a version not in the compat table.
func TestInspect_SchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	// Write a DB with schema_version=99 (unknown).
	dbPath := buildDB(t, dir, "0.4.8", 99)
	mfst := buildManifest(99, nil)
	artPath := buildArtifact(t, dir, mfst, dbPath)

	r, err := Inspect(context.Background(), artPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	schemaCheck := r.Checks[4]
	if schemaCheck.Name != "schema_version" {
		t.Fatalf("expected check[4]=schema_version, got %q", schemaCheck.Name)
	}
	if schemaCheck.Pass {
		t.Error("schema_version should fail for unknown version 99")
	}
	if schemaCheck.Detail == "" {
		t.Error("schema_version detail should describe the failure")
	}
}

// TestInspect_ManifestSchemaVersionMismatch verifies check (e) fails when
// manifest.SchemaVersion != _schema.schema_version.
func TestInspect_ManifestSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	dbPath := buildDB(t, dir, "0.4.8", model.SchemaVersion)
	// Manifest claims a different schema version.
	mfst := buildManifest(model.SchemaVersion+1, nil)
	artPath := buildArtifact(t, dir, mfst, dbPath)

	r, err := Inspect(context.Background(), artPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	schemaCheck := r.Checks[4]
	if schemaCheck.Pass {
		t.Error("schema_version should fail when manifest and _schema differ")
	}
}

// TestInspect_NonExistentArtifact verifies Inspect returns a failing Report
// with a tar_integrity fail when the file does not exist (not a top-level I/O error,
// since the caller is expected to stat the file first at the cmd layer).
func TestInspect_NonExistentArtifact(t *testing.T) {
	r, err := Inspect(context.Background(), "/nonexistent/path.tar.gz")
	if err != nil {
		// error from MkdirTemp is the only top-level I/O error; open failure
		// is a check-level failure, not a top-level one.
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if r.OK {
		t.Error("expected OK=false for nonexistent artifact")
	}
	if r.Checks[0].Pass {
		t.Error("tar_integrity should fail for nonexistent file")
	}
}

// TestIsKnownSchema and TestSupportedBinaries cover the compat helpers.
func TestIsKnownSchema(t *testing.T) {
	if !IsKnownSchema(1) {
		t.Error("schema 1 should be known")
	}
	if !IsKnownSchema(2) {
		t.Error("schema 2 should be known")
	}
	if IsKnownSchema(99) {
		t.Error("schema 99 should not be known")
	}
}

func TestSupportedBinaries(t *testing.T) {
	bins := SupportedBinaries(1)
	if len(bins) == 0 {
		t.Error("schema 1 should have supported binaries")
	}
	bins2 := SupportedBinaries(99)
	if bins2 != nil {
		t.Errorf("schema 99 should return nil, got %v", bins2)
	}
}

// TestFormatBytes covers the human-readable byte formatter.
func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}
	for _, tc := range cases {
		got := formatBytes(tc.n)
		if got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
