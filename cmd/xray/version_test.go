package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestVersionCmd(t *testing.T) {
	root, out, errBuf := newTestRoot(t)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("version returned err: %v", err)
	}

	got := out.String()
	// Build-time vars are unset in test → defaults from main.go.
	want := "xray dev (commit none, built unknown)\n"
	if got != want {
		t.Errorf("version stdout = %q, want %q", got, want)
	}

	// Also assert against the documented pattern, so renaming the default
	// values does not silently break the contract.
	re := regexp.MustCompile(`^xray \S+ \(commit \S+, built \S+\)\n$`)
	if !re.MatchString(got) {
		t.Errorf("version stdout %q does not match documented pattern", got)
	}

	if s := errBuf.String(); strings.TrimSpace(s) != "" {
		t.Errorf("version stderr = %q, want empty", s)
	}
}
