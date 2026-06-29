package connector

import "strings"

// IsFullSHA reports whether s is a valid 40-character hex git commit SHA.
// Both lowercase and uppercase hex digits are accepted.
func IsFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// NormalizeFullSHA returns the lowercase form of s and true when s is a valid
// 40-character hex git commit SHA; otherwise it returns ("", false).
// Use this instead of IsFullSHA when the result is stored or compared — all
// SHA columns in the schema are lowercase, so callers must not store
// uppercase variants.
func NormalizeFullSHA(s string) (string, bool) {
	if !IsFullSHA(s) {
		return "", false
	}
	return strings.ToLower(s), true
}
