package github

import "testing"

func TestIsBot(t *testing.T) {
	cases := []struct {
		handle string
		want   bool
	}{
		{"dependabot[bot]", true},
		{"renovate[bot]", true},
		{"Dependabot[bot]", true},
		{"alice", false},
		{"", false},
		{"github-actions[bot]", true},
	}
	for _, tc := range cases {
		if got := isBot(tc.handle); got != tc.want {
			t.Errorf("isBot(%q) = %v, want %v", tc.handle, got, tc.want)
		}
	}
}

func TestKindFor(t *testing.T) {
	cases := []struct {
		handle, email, want string
	}{
		{"alice", "alice@example.com", "human"},
		{"dependabot[bot]", "noreply@github.com", "bot"},
		{"copilot[bot]", "copilot[bot]@users.noreply.github.com", "ai_tool"},
		{"someone", "noreply@anthropic.com", "ai_tool"},
		{"someone", "NoReply@Cursor.com", "ai_tool"},
		{"someone", "noreply@aider.chat", "ai_tool"},
		{"copilot[bot]", "noreply@github.com", "ai_tool"},
		{"github-actions[bot]", "actions@github.com", "bot"},
	}
	for _, tc := range cases {
		if got := kindFor(tc.handle, tc.email); got != tc.want {
			t.Errorf("kindFor(%q, %q) = %q, want %q", tc.handle, tc.email, got, tc.want)
		}
	}
}
