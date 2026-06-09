package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "x.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	return p
}

func TestValidateCmd_HappyPath(t *testing.T) {
	p := writeTOML(t, validTOML)

	root, out, errBuf := newTestRoot(t)
	root.SetArgs([]string{"validate", p})

	if err := root.Execute(); err != nil {
		t.Fatalf("validate returned err: %v (stderr=%q)", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "ok  config valid") {
		t.Errorf("validate stdout = %q, want ok message", out.String())
	}
	if errBuf.Len() != 0 {
		t.Errorf("validate stderr = %q, want empty", errBuf.String())
	}
}

func TestValidateCmd_BadWindow(t *testing.T) {
	body := `window = "2025-06-30..2025-01-01"

[teams]
unassigned = ["kmcd/foo"]
`
	p := writeTOML(t, body)

	root, _, errBuf := newTestRoot(t)
	root.SetArgs([]string{"validate", p})

	err := root.Execute()
	if err == nil {
		t.Fatal("validate err = nil, want non-nil for backwards window")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "window: end date precedes start date") {
		t.Errorf("stderr = %q, want window-precedes diagnostic", errBuf.String())
	}
}

func TestValidateCmd_MissingTeams(t *testing.T) {
	body := `window = "2025-01-01..2025-06-30"
`
	p := writeTOML(t, body)

	root, _, errBuf := newTestRoot(t)
	root.SetArgs([]string{"validate", p})

	err := root.Execute()
	if err == nil {
		t.Fatal("validate err = nil, want non-nil with no teams")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "teams") {
		t.Errorf("stderr = %q, want teams diagnostic", errBuf.String())
	}
}

func TestValidateCmd_FileNotFound(t *testing.T) {
	root, _, _ := newTestRoot(t)
	root.SetArgs([]string{"validate", filepath.Join(t.TempDir(), "does-not-exist.toml")})

	err := root.Execute()
	if err == nil {
		t.Fatal("validate err = nil, want non-nil for missing file")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestValidateCmd_QuietSuppressesSuccessLine(t *testing.T) {
	p := writeTOML(t, validTOML)
	root, stdout, stderr := newTestRoot(t)
	root.SetArgs([]string{"validate", p, "--output", "quiet"})
	if err := root.Execute(); err != nil {
		t.Fatalf("validate err: %v (stderr=%q)", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("quiet stdout = %q, want empty", stdout.String())
	}
}

func TestValidateCmd_DefaultsToXrayToml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xray.toml"), []byte(validTOML), 0o600); err != nil {
		t.Fatalf("write xray.toml: %v", err)
	}
	t.Chdir(dir)

	root, out, errBuf := newTestRoot(t)
	root.SetArgs([]string{"validate"})

	if err := root.Execute(); err != nil {
		t.Fatalf("validate (no arg) returned err: %v (stderr=%q)", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "ok  config valid") {
		t.Errorf("stdout = %q, want ok message", out.String())
	}
}

func TestValidateCmd_MissingDefaultReportsSpecificError(t *testing.T) {
	t.Chdir(t.TempDir())

	root, _, errBuf := newTestRoot(t)
	root.SetArgs([]string{"validate"})

	err := root.Execute()
	if err == nil {
		t.Fatal("validate err = nil, want non-nil with no xray.toml in cwd")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "xray.toml not found in current directory; pass a path or run `xray init`") {
		t.Errorf("stderr = %q, want specific missing-default diagnostic", errBuf.String())
	}
}

func TestValidateCmd_JSONEmitsSummary(t *testing.T) {
	p := writeTOML(t, validTOML)
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"validate", p, "--output", "json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("validate err: %v", err)
	}
	line := strings.TrimSpace(stdout.String())
	if !strings.Contains(line, `"kind":"validate_summary"`) {
		t.Errorf("stdout = %q, want validate_summary line", line)
	}
	if !strings.Contains(line, `"ok":true`) {
		t.Errorf("stdout = %q, want ok=true", line)
	}
	if !strings.Contains(line, `"config_path":"`+p) {
		t.Errorf("stdout = %q, want config_path containing %q", line, p)
	}
}
