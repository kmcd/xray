package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// newTestRoot constructs the root cobra command with stdout and stderr
// rerouted to in-memory buffers. It is the shared entry point for every
// cmd-level test in this package.
func newTestRoot(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	return root, &stdout, &stderr
}

// validTOML is a minimal config that satisfies every required schema rule:
// a parseable window and one team with one repo. It is intentionally free
// of optional connector blocks so individual tests can append what they need.
const validTOML = `window = "2025-01-01..2025-06-30"

[teams]
unassigned = ["kmcd/foo"]
`
