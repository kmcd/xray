// Package inspect implements post-hoc artifact validation for xray .tar.gz
// artifacts. It runs five checks in order and returns a Report regardless of
// whether individual checks pass or fail; the error return is reserved for
// I/O failures that prevent the inspection from running at all.
package inspect

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/kmcd/xray/internal/manifest"
	// Pure-Go SQLite driver; CGO_ENABLED=0.
	_ "modernc.org/sqlite"
)

// Report is the top-level result of an Inspect call. The Checks slice always
// contains exactly five entries in order: tar_integrity, manifest_shape,
// sqlite_integrity, row_counts, schema_version. Consumers can rely on the
// index position and the Name field being stable.
type Report struct {
	Artifact string  `json:"artifact"`
	OK       bool    `json:"ok"`
	Checks   []Check `json:"checks"`
}

// Check is one of the five validation steps.
type Check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	// Skipped is true when the check was not run because an earlier check failed.
	// A skipped check always has Pass=false; Skipped distinguishes "did not run"
	// from "ran and failed" for automated consumers.
	Skipped bool `json:"skipped,omitempty"`
	// Detail is a human-readable summary; empty is allowed on pass.
	Detail string `json:"detail"`
	// Mismatches is populated only by the row_counts check.
	Mismatches []CountMismatch `json:"mismatches,omitempty"`
}

// CountMismatch is a single (table, manifest count, db count) divergence row
// reported by the row_counts check.
type CountMismatch struct {
	Table    string `json:"table"`
	Manifest int    `json:"manifest"`
	DB       int    `json:"db"`
}

// validTableName matches the safe subset of SQLite identifiers we accept from
// the manifest Counts map. All real xray table names match this pattern.
var validTableName = regexp.MustCompile(`^[a-z_]+$`)

// Inspect runs the five validation checks against artifactPath and returns a
// Report. The error return is reserved for failures that prevent the
// inspection from starting at all (artifact does not exist, cannot create
// temp dir); per-check failures populate Report.Checks.
func Inspect(ctx context.Context, artifactPath string) (*Report, error) {
	r := &Report{Artifact: artifactPath}

	tmpDir, err := os.MkdirTemp("", "xray-inspect-*")
	if err != nil {
		return nil, fmt.Errorf("inspect: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Run checks in order. After check (a) fails we cannot open the SQLite
	// DB, so later checks are marked skipped but still emitted to keep the
	// JSON shape stable.
	var mfst *manifest.Manifest
	var tmpDBPath string

	// (a) Tar integrity
	tarCheck, mfstBytes, dbPath, tarOK := checkTarIntegrity(ctx, artifactPath, tmpDir)
	r.Checks = append(r.Checks, tarCheck)
	if tarOK {
		tmpDBPath = dbPath
		// (b) Manifest shape
		mfstCheck, m, mfstOK := checkManifestShape(mfstBytes)
		r.Checks = append(r.Checks, mfstCheck)
		if mfstOK {
			mfst = m
		}
	} else {
		r.Checks = append(r.Checks, skipCheck("manifest_shape", "skipped: tar integrity failed"))
	}

	if tmpDBPath != "" {
		// (c) SQLite integrity
		r.Checks = append(r.Checks, checkSQLiteIntegrity(ctx, tmpDBPath))

		// (d) Row counts
		r.Checks = append(r.Checks, checkRowCounts(ctx, tmpDBPath, mfst))

		// (e) Schema version
		r.Checks = append(r.Checks, checkSchemaVersion(ctx, tmpDBPath, mfst))
	} else {
		r.Checks = append(r.Checks, skipCheck("sqlite_integrity", "skipped: tar integrity failed"))
		r.Checks = append(r.Checks, skipCheck("row_counts", "skipped: tar integrity failed"))
		r.Checks = append(r.Checks, skipCheck("schema_version", "skipped: tar integrity failed"))
	}

	r.OK = allPass(r.Checks)
	return r, nil
}

// checkTarIntegrity streams the entire .tar.gz, validates gzip+tar checksums
// end-to-end, and extracts manifest.json into memory and metrics.sqlite to
// a temp file. Returns the check, the manifest bytes, the temp db path, and
// whether the check passed.
func checkTarIntegrity(ctx context.Context, artifactPath, tmpDir string) (Check, []byte, string, bool) {
	// #nosec G304 -- operator-supplied artifact path; validated by caller.
	f, err := os.Open(artifactPath)
	if err != nil {
		return failCheck("tar_integrity", fmt.Sprintf("open: %v", err)), nil, "", false
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return failCheck("tar_integrity", fmt.Sprintf("gzip header: %v", err)), nil, "", false
	}
	defer func() {
		// gzip.Reader.Close() validates the CRC32 and size trailer.
		_ = gz.Close()
	}()

	tr := tar.NewReader(gz)

	var mfstBuf bytes.Buffer
	var tmpDBPath string
	var members int
	var bytesRead int64
	seenManifest := false
	seenDB := false

	for {
		if err := ctx.Err(); err != nil {
			return failCheck("tar_integrity", fmt.Sprintf("cancelled: %v", err)), nil, "", false
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return failCheck("tar_integrity", fmt.Sprintf("tar next: %v", err)), nil, "", false
		}
		members++

		switch hdr.Name {
		case "manifest.json":
			if seenManifest {
				return failCheck("tar_integrity", "duplicate manifest.json"), nil, "", false
			}
			seenManifest = true
			n, err := io.Copy(&mfstBuf, tr) //nolint:gosec // G110: manifest.json is small; bounded by the tar header size.
			if err != nil {
				return failCheck("tar_integrity", fmt.Sprintf("read manifest.json: %v", err)), nil, "", false
			}
			bytesRead += n

		case "metrics.sqlite":
			if seenDB {
				return failCheck("tar_integrity", "duplicate metrics.sqlite"), nil, "", false
			}
			seenDB = true
			dbPath := tmpDir + "/metrics.sqlite"
			// #nosec G304 -- path is under our own MkdirTemp directory.
			dbFile, err := os.Create(dbPath)
			if err != nil {
				return failCheck("tar_integrity", fmt.Sprintf("create temp db: %v", err)), nil, "", false
			}
			n, copyErr := io.Copy(dbFile, tr) //nolint:gosec // G110: operator-supplied artifact; extraction is intentional.
			_ = dbFile.Close()
			if copyErr != nil {
				return failCheck("tar_integrity", fmt.Sprintf("extract metrics.sqlite: %v", copyErr)), nil, "", false
			}
			bytesRead += n
			tmpDBPath = dbPath

		default:
			// Discard unknown members but count their bytes to force full
			// CRC validation of every block in the archive.
			n, err := io.Copy(io.Discard, tr) //nolint:gosec // G110: discarding unknown members to force CRC validation.
			if err != nil {
				return failCheck("tar_integrity", fmt.Sprintf("read %s: %v", hdr.Name, err)), nil, "", false
			}
			bytesRead += n
		}
	}

	// Validate gzip CRC by closing the reader now (deferred close ignores error).
	if err := gz.Close(); err != nil {
		return failCheck("tar_integrity", fmt.Sprintf("gzip CRC: %v", err)), nil, "", false
	}

	if !seenManifest {
		return failCheck("tar_integrity", "manifest.json not found in archive"), nil, "", false
	}
	if !seenDB {
		return failCheck("tar_integrity", "metrics.sqlite not found in archive"), nil, "", false
	}

	detail := fmt.Sprintf("%d members, %s read", members, formatBytes(bytesRead))
	return passCheck("tar_integrity", detail), mfstBuf.Bytes(), tmpDBPath, true
}

// checkManifestShape unmarshals manifest.json bytes and verifies required fields.
func checkManifestShape(data []byte) (Check, *manifest.Manifest, bool) {
	var m manifest.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return failCheck("manifest_shape", fmt.Sprintf("json unmarshal: %v", err)), nil, false
	}
	if m.ToolVersion == "" {
		return failCheck("manifest_shape", "missing field: tool_version"), nil, false
	}
	if m.SchemaVersion == 0 {
		return failCheck("manifest_shape", "missing field: schema_version"), nil, false
	}
	if m.RunID == "" {
		return failCheck("manifest_shape", "missing field: run_id"), nil, false
	}
	detail := fmt.Sprintf("tool_version=%s schema_version=%d run_id=%s",
		m.ToolVersion, m.SchemaVersion, m.RunID)
	return passCheck("manifest_shape", detail), &m, true
}

// checkSQLiteIntegrity runs PRAGMA integrity_check and reports the result.
func checkSQLiteIntegrity(ctx context.Context, dbPath string) Check {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return failCheck("sqlite_integrity", fmt.Sprintf("open: %v", err))
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return failCheck("sqlite_integrity", fmt.Sprintf("pragma: %v", err))
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return failCheck("sqlite_integrity", fmt.Sprintf("scan: %v", err))
		}
		lines = append(lines, s)
		if len(lines) >= 5 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return failCheck("sqlite_integrity", fmt.Sprintf("rows: %v", err))
	}

	if len(lines) == 1 && lines[0] == "ok" {
		return passCheck("sqlite_integrity", "")
	}
	return failCheck("sqlite_integrity", strings.Join(lines, "; "))
}

// checkRowCounts compares manifest.Counts against live COUNT(*) for each table.
func checkRowCounts(ctx context.Context, dbPath string, m *manifest.Manifest) Check {
	if m == nil {
		return skipCheck("row_counts", "skipped: manifest not available")
	}
	if len(m.Counts) == 0 {
		return passCheck("row_counts", "0 tables reconciled (empty manifest counts)")
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return failCheck("row_counts", fmt.Sprintf("open: %v", err))
	}
	defer db.Close()

	// Enumerate known tables for whitelist + unknown-table detection.
	knownTables, err := listTables(ctx, db)
	if err != nil {
		return failCheck("row_counts", fmt.Sprintf("list tables: %v", err))
	}

	var mismatches []CountMismatch
	var unknownTables []string
	reconciled := 0

	for table, expected := range m.Counts {
		if !validTableName.MatchString(table) {
			return failCheck("row_counts", fmt.Sprintf("unsafe table name in manifest: %q", table))
		}
		if !knownTables[table] {
			unknownTables = append(unknownTables, table)
			continue
		}
		// Table name validated via whitelist above; safe to interpolate.
		var got int
		row := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table) //nolint:gosec
		if err := row.Scan(&got); err != nil {
			return failCheck("row_counts", fmt.Sprintf("count %s: %v", table, err))
		}
		reconciled++
		if got != expected {
			mismatches = append(mismatches, CountMismatch{Table: table, Manifest: expected, DB: got})
		}
	}

	if len(unknownTables) > 0 {
		// Surface unknown tables as a failure so schema drift is observable.
		return Check{
			Name:   "row_counts",
			Pass:   false,
			Detail: fmt.Sprintf("manifest references tables not in DB: %s", strings.Join(unknownTables, ", ")),
		}
	}

	if len(mismatches) > 0 {
		parts := make([]string, 0, len(mismatches))
		for _, mm := range mismatches {
			parts = append(parts, fmt.Sprintf("table=%s manifest=%d db=%d", mm.Table, mm.Manifest, mm.DB))
		}
		return Check{
			Name:       "row_counts",
			Pass:       false,
			Detail:     strings.Join(parts, "; "),
			Mismatches: mismatches,
		}
	}

	return passCheck("row_counts", fmt.Sprintf("%d tables reconciled", reconciled))
}

// checkSchemaVersion validates the _schema table against the manifest and
// the compat table.
func checkSchemaVersion(ctx context.Context, dbPath string, m *manifest.Manifest) Check {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return failCheck("schema_version", fmt.Sprintf("open: %v", err))
	}
	defer db.Close()

	// Defensive: _schema may have >1 row if a future bug writes duplicates.
	var dbVersion int
	var rowCount int
	rows, err := db.QueryContext(ctx, "SELECT schema_version FROM _schema ORDER BY applied_at DESC LIMIT 1")
	if err != nil {
		return failCheck("schema_version", fmt.Sprintf("query _schema: %v", err))
	}
	defer rows.Close()
	for rows.Next() {
		rowCount++
		if err := rows.Scan(&dbVersion); err != nil {
			return failCheck("schema_version", fmt.Sprintf("scan _schema: %v", err))
		}
	}
	if err := rows.Err(); err != nil {
		return failCheck("schema_version", fmt.Sprintf("rows _schema: %v", err))
	}
	if rowCount == 0 {
		return failCheck("schema_version", "_schema table is empty")
	}

	// Count total rows to surface potential duplicates.
	var totalRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM _schema").Scan(&totalRows); err != nil {
		return failCheck("schema_version", fmt.Sprintf("count _schema: %v", err))
	}

	// (i) Manifest vs _schema mismatch.
	if m != nil && m.SchemaVersion != 0 && m.SchemaVersion != dbVersion {
		return failCheck("schema_version",
			fmt.Sprintf("manifest=%d, _schema=%d", m.SchemaVersion, dbVersion))
	}

	// (ii) Compat lookup.
	if !IsKnownSchema(dbVersion) {
		return failCheck("schema_version",
			fmt.Sprintf("unknown schema_version=%d (recognised: %v)", dbVersion, recognisedVersions()))
	}

	bins := SupportedBinaries(dbVersion)
	detail := fmt.Sprintf("schema_version=%d (xray %s)", dbVersion, strings.Join(bins, ", "))
	if totalRows > 1 {
		detail += fmt.Sprintf(" [warning: _schema has %d rows]", totalRows)
	}
	return passCheck("schema_version", detail)
}

// recognisedVersions returns a sorted list of known schema versions for
// error messages.
func recognisedVersions() []int {
	out := make([]int, 0, len(SchemaCompat))
	for v := range SchemaCompat {
		out = append(out, v)
	}
	// Simple insertion sort is fine for a handful of entries.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// listTables returns a set of table names present in the SQLite database.
func listTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables[name] = true
	}
	return tables, rows.Err()
}

// passCheck constructs a passing Check.
func passCheck(name, detail string) Check {
	return Check{Name: name, Pass: true, Detail: detail}
}

// failCheck constructs a failing Check.
func failCheck(name, detail string) Check {
	return Check{Name: name, Pass: false, Detail: detail}
}

// skipCheck constructs a Check that was not run because an earlier check failed.
func skipCheck(name, detail string) Check {
	return Check{Name: name, Pass: false, Skipped: true, Detail: detail}
}

// allPass reports whether every check in the slice passed.
func allPass(checks []Check) bool {
	for _, c := range checks {
		if !c.Pass {
			return false
		}
	}
	return true
}

// formatBytes renders a byte count as a human-readable string.
func formatBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.1f GB", float64(n)/GB)
	case n >= MB:
		return fmt.Sprintf("%.1f MB", float64(n)/MB)
	case n >= KB:
		return fmt.Sprintf("%.1f KB", float64(n)/KB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
