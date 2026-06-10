package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsTLSCertError(t *testing.T) {
	uae := x509.UnknownAuthorityError{}
	// Wrapping chain: url.Error → fmt.Errorf %w → x509.UnknownAuthorityError
	wrapped := &url.Error{Op: "Get", URL: "https://api.github.com", Err: fmt.Errorf("TLS: %w", uae)}

	if !isTLSCertError(uae) {
		t.Error("isTLSCertError(UnknownAuthorityError) = false, want true")
	}
	if !isTLSCertError(wrapped) {
		t.Error("isTLSCertError(url.Error wrapping UnknownAuthorityError) = false, want true")
	}
	if isTLSCertError(errors.New("connection refused")) {
		t.Error("isTLSCertError(connection refused) = true, want false")
	}
	if isTLSCertError(errors.New("x509: certificate has expired")) {
		t.Error("isTLSCertError(unrelated x509 string) = true, want false")
	}
}

func TestCheckCmd(t *testing.T) {
	// `check` does a real `git ls-remote` for every repo. We can not safely
	// assume network reachability of a real GitHub repo from CI, so we
	// expect a non-zero exit from the clone-access step. What we *can*
	// reliably assert is that the config-valid and git-on-PATH lines both
	// land on stdout — those are local checks under our control.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; check requires it")
	}

	p := writeTOML(t, validTOML)

	root, out, _ := newTestRoot(t)
	root.SetArgs([]string{"check", p})

	// We do not assert on err: the ls-remote against kmcd/foo may either
	// succeed (in a network-enabled environment with credentials) or fail
	// (offline). Either way, the two deterministic stdout lines must be
	// present before the network step runs.
	_ = root.Execute()

	got := out.String()
	if !strings.Contains(got, "ok  config valid") {
		t.Errorf("check stdout missing config-valid line: %q", got)
	}
	if !strings.Contains(got, "ok  git") {
		t.Errorf("check stdout missing git-on-PATH line: %q", got)
	}
}

func TestCheckCmd_QuietSuppressesSuccessLines(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; check requires it")
	}
	p := writeTOML(t, validTOML)
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"check", p, "--output", "quiet"})
	_ = root.Execute()
	// In quiet mode no success lines land on stdout regardless of ls-remote
	// outcome. The fact that the validation/git-on-PATH steps would have
	// printed in default mode is enough to verify suppression.
	if strings.Contains(stdout.String(), "ok  config valid") {
		t.Errorf("quiet stdout contained config-valid line: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "ok  git") {
		t.Errorf("quiet stdout contained git-on-PATH line: %q", stdout.String())
	}
}

func TestCheckCmd_JSONEmitsSummary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; check requires it")
	}
	p := writeTOML(t, validTOML)
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"check", p, "--output", "json", "--no-cost-preview"})
	_ = root.Execute()
	out := strings.TrimSpace(stdout.String())
	// Exactly one JSON line on stdout regardless of ls-remote outcome.
	if !strings.Contains(out, `"kind":"check_summary"`) {
		t.Errorf("stdout = %q, want check_summary line", out)
	}
	// Newline-terminated single object (no NDJSON banter).
	if strings.Count(out, "\n") != 0 {
		t.Errorf("stdout has %d newlines, want a single line", strings.Count(out, "\n"))
	}
}

func TestCheckCmd_DefaultsToXrayToml(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; check requires it")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xray.toml"), []byte(validTOML), 0o600); err != nil {
		t.Fatalf("write xray.toml: %v", err)
	}
	t.Chdir(dir)

	root, out, _ := newTestRoot(t)
	root.SetArgs([]string{"check", "--no-cost-preview"})
	_ = root.Execute() // ls-remote outcome irrelevant; deterministic lines suffice

	got := out.String()
	if !strings.Contains(got, "ok  config valid") {
		t.Errorf("check (no arg) stdout missing config-valid line: %q", got)
	}
}

func TestCheckCmd_MissingDefaultReportsSpecificError(t *testing.T) {
	t.Chdir(t.TempDir())

	root, _, errBuf := newTestRoot(t)
	root.SetArgs([]string{"check"})

	err := root.Execute()
	if err == nil {
		t.Fatal("check err = nil, want non-nil with no xray.toml in cwd")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "xray.toml not found in current directory; pass a path or run `xray init`") {
		t.Errorf("stderr = %q, want specific missing-default diagnostic", errBuf.String())
	}
}

func TestCheckCmd_NoCostPreviewFlag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; check requires it")
	}
	p := writeTOML(t, validTOML)
	root, stdout, _ := newTestRoot(t)
	root.SetArgs([]string{"check", p, "--no-cost-preview"})
	_ = root.Execute()
	if strings.Contains(stdout.String(), "Plan") {
		t.Errorf("Plan block present despite --no-cost-preview: %q", stdout.String())
	}
}
