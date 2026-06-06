package main

import (
	"os/exec"
	"strings"
	"testing"
)

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
