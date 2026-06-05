package github

import "testing"

func TestParseSubjectRevert(t *testing.T) {
	cases := []struct {
		subject string
		want    bool
	}{
		{"Revert \"add foo\"", true},
		{"Revert add foo", true},
		{"revert foo", false},
		{"fix: revert behaviour", false},
	}
	for _, tc := range cases {
		if got := parseSubjectRevert(tc.subject); got != tc.want {
			t.Errorf("parseSubjectRevert(%q) = %v, want %v", tc.subject, got, tc.want)
		}
	}
}

func TestParseRevertsSHA(t *testing.T) {
	body := "feat: add stuff\n\nThis reverts commit deadbeefcafe1234567890abcdef12345678ab.\n"
	got := parseRevertsSHA(body)
	if got != "deadbeefcafe1234567890abcdef12345678ab" {
		t.Errorf("parseRevertsSHA = %q", got)
	}
	if parseRevertsSHA("nothing here") != "" {
		t.Errorf("expected empty for non-revert body")
	}
	// Short SHA.
	if got := parseRevertsSHA("This reverts commit abc1234"); got != "abc1234" {
		t.Errorf("short sha: got %q", got)
	}
}

func TestParseIsRevert(t *testing.T) {
	if !parseIsRevert("Revert \"bad change\"", "") {
		t.Error("subject revert not detected")
	}
	if !parseIsRevert("feat: x", "This reverts commit abcdef0") {
		t.Error("body revert not detected")
	}
	if parseIsRevert("feat: x", "no signals here") {
		t.Error("false positive on clean commit")
	}
}

func TestParseHasHotfixMarker(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"normal commit message", false},
		{"hotfix: pager going off", true},
		{"this is URGENT", true},
		{"WIP — not done", true},
		{"contains TODO", true},
		{"FIXME later", true},
		{"untested but shipping", true},
		{"that was a hack", true},
		{"temporary workaround", true},
		// word boundary: "hackathon" must not match "hack"
		{"team hackathon", false},
	}
	for _, tc := range cases {
		if got := parseHasHotfixMarker(tc.body); got != tc.want {
			t.Errorf("parseHasHotfixMarker(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
