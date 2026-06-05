package github

import (
	"regexp"
	"strings"
)

// hotfixRe matches the risk-marker keyword set on commit and PR bodies.
// Case-insensitive; word boundaries keep "hackathon" from matching "hack".
var hotfixRe = regexp.MustCompile(`(?i)\b(hotfix|urgent|wip|untested|temporary|hack|todo|fixme)\b`)

// revertBodyRe matches the canonical revert body line emitted by `git revert`
// ("This reverts commit <sha>."). Captures the SHA.
var revertBodyRe = regexp.MustCompile(`(?m)^\s*This reverts commit\s+([0-9a-fA-F]{7,40})\b`)

// parseSubjectRevert returns true if subject begins with a literal
// "Revert " prefix (the convention used by `git revert` and the GitHub
// "Revert" button).
func parseSubjectRevert(subject string) bool {
	return strings.HasPrefix(subject, "Revert ")
}

// parseRevertsSHA returns the SHA the body's "This reverts commit ..." line
// refers to, or empty if no such line is present.
func parseRevertsSHA(body string) string {
	m := revertBodyRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

// parseIsRevert covers both signals: subject prefix or body line.
func parseIsRevert(subject, body string) bool {
	if parseSubjectRevert(subject) {
		return true
	}
	return parseRevertsSHA(body) != ""
}

// parseHasHotfixMarker returns true when any risk-marker keyword appears
// in the commit body (case-insensitive).
func parseHasHotfixMarker(body string) bool {
	return hotfixRe.MatchString(body)
}
