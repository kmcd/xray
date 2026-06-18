package githubactions

import "testing"

func TestIsNonTerminalState(t *testing.T) {
	terminal := []string{"success", "failure", "error", "failed", "cancelled", "timed_out"}
	for _, s := range terminal {
		if isNonTerminalState(s) {
			t.Errorf("isNonTerminalState(%q) = true, want false", s)
		}
	}
	nonTerminal := []string{"", "in_progress", "queued", "pending", "waiting"}
	for _, s := range nonTerminal {
		if !isNonTerminalState(s) {
			t.Errorf("isNonTerminalState(%q) = false, want true", s)
		}
	}
}
