package github

import "strings"

// aiToolEmails maps known AI-tool noreply addresses to "ai_tool" kind for
// commit coauthor classification. Lowercase matches only.
var aiToolEmails = map[string]bool{
	"noreply@anthropic.com":                      true,
	"noreply@cursor.com":                         true,
	"copilot[bot]@users.noreply.github.com":      true,
	"noreply@aider.chat":                         true,
}

// isBot returns true when handle looks like a GitHub bot account. The
// convention is a trailing "[bot]" suffix (e.g. "dependabot[bot]").
func isBot(handle string) bool {
	h := strings.ToLower(strings.TrimSpace(handle))
	return strings.HasSuffix(h, "[bot]")
}

// kindFor classifies a coauthor identity into "human" / "bot" / "ai_tool".
// The handle suffix wins for bot detection; email matches against the
// known AI-tool noreply set escalate to "ai_tool". Otherwise "human".
func kindFor(handle, email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if aiToolEmails[e] {
		return "ai_tool"
	}
	if isBot(handle) {
		// Copilot bot identifies via [bot] suffix; only escalate to ai_tool
		// when the email is one of the known noreply addresses above.
		if strings.HasPrefix(strings.ToLower(handle), "copilot") {
			return "ai_tool"
		}
		return "bot"
	}
	return "human"
}
