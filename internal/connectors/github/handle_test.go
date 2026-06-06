package github

import (
	"regexp"
	"testing"
)

var handleShape = regexp.MustCompile(`^h_\d{15}$`)

func TestHashHandle_Shape(t *testing.T) {
	cases := []string{
		"Alice Smith <alice@example.com>",
		"@alice",
		"committer <bot@github.com>",
		"single-word",
	}
	for _, in := range cases {
		got := hashHandle(in)
		if !handleShape.MatchString(got) {
			t.Errorf("hashHandle(%q) = %q, want shape %s", in, got, handleShape)
		}
	}
}

func TestHashHandle_Empty(t *testing.T) {
	if got := hashHandle(""); got != "" {
		t.Errorf("hashHandle(\"\") = %q, want empty", got)
	}
	if got := hashHandle("   "); got != "" {
		t.Errorf("hashHandle(whitespace) = %q, want empty", got)
	}
}

func TestHashHandle_CaseInsensitive(t *testing.T) {
	a := hashHandle("Alice <ALICE@example.com>")
	b := hashHandle("alice <alice@example.com>")
	if a != b {
		t.Errorf("case mismatch: %q vs %q", a, b)
	}
}

func TestHashHandle_DistinctNamespaces(t *testing.T) {
	// A login "alice" must hash differently from a commit ident "alice"
	// (no email). The "@" prefix in canonicalLogin keeps the two surfaces
	// separate; fusing them would silently merge unrelated people.
	loginHash := hashHandle(canonicalLogin("alice"))
	commitHash := hashHandle(canonicalCommitIdent("alice", ""))
	if loginHash == commitHash {
		t.Errorf("login and commit-ident namespaces collided: both %q", loginHash)
	}
}

func TestCanonicalCommitIdent(t *testing.T) {
	cases := []struct {
		name, email, want string
	}{
		{"Alice", "alice@example.com", "Alice <alice@example.com>"},
		{"  Alice  ", "  alice@example.com  ", "Alice <alice@example.com>"},
		{"Alice", "", "Alice"},
		{"", "alice@example.com", "<alice@example.com>"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := canonicalCommitIdent(tc.name, tc.email); got != tc.want {
			t.Errorf("canonicalCommitIdent(%q, %q) = %q, want %q", tc.name, tc.email, got, tc.want)
		}
	}
}

func TestCanonicalLogin(t *testing.T) {
	if got := canonicalLogin("Alice"); got != "@alice" {
		t.Errorf("canonicalLogin(\"Alice\") = %q, want @alice", got)
	}
	if got := canonicalLogin("  bob  "); got != "@bob" {
		t.Errorf("canonicalLogin trim failed: %q", got)
	}
	if got := canonicalLogin(""); got != "" {
		t.Errorf("canonicalLogin(\"\") = %q, want empty", got)
	}
}
