package github

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

// hashHandle returns the opaque "h_<digits>" token assay enforces against
// `^h_\d{3,}$`. canonical is the input string that has already been resolved
// through the mailmap; downstream code must not pass raw aliases.
//
// The hash is the low 64 bits of sha256(lowercased canonical) reduced modulo
// 10^15 and zero-padded to 15 digits. 10^15 gives birthday-collision parity
// near 10^7.5 distinct authors — well above any plausible team-of-teams scale
// while keeping the token short enough to read at a glance.
//
// Empty input returns empty string so NULL semantics survive: a commit with
// no author identity still emits NULL, not a hash of the empty string.
func hashHandle(canonical string) string {
	canonical = strings.TrimSpace(canonical)
	if canonical == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(canonical)))
	u := binary.BigEndian.Uint64(sum[:8]) % 1_000_000_000_000_000
	return fmt.Sprintf("h_%015d", u)
}

// canonicalCommitIdent renders the canonical "Name <email>" string for a
// commit's author or committer identity. Both fields trimmed; an empty email
// degrades to "Name" without the angle brackets (rare; protects against
// "Name <>" hashing differently from "Name").
func canonicalCommitIdent(name, email string) string {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" && email == "" {
		return ""
	}
	if email == "" {
		return name
	}
	if name == "" {
		return "<" + email + ">"
	}
	return name + " <" + email + ">"
}

// canonicalLogin renders a GitHub login as the input for hashHandle. GitHub
// logins are case-insensitive at the API surface; lowercasing here means
// "Alice" and "alice" hash identically. The "@" prefix disambiguates from
// the commit-ident namespace so a login "alice" and a commit name "alice"
// (with no email) hash to distinct values — the two identifier surfaces
// don't link reliably without per-user email resolution, and pretending
// they do would silently fuse unrelated people.
func canonicalLogin(login string) string {
	login = strings.TrimSpace(login)
	if login == "" {
		return ""
	}
	return "@" + strings.ToLower(login)
}
