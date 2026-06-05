package github

import "testing"

func TestParseCodeowners(t *testing.T) {
	body := `# Default owners
*       @alice @kmcd/platform

# Frontend
/web/   @bob

# Inline comment
/api/   @carol # the api owner

# Blank lines and pure comments below:

# nothing here
`
	rows := parseCodeowners("kmcd/foo", body)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}

	// Row 0: pattern "*", handle "alice", user
	if rows[0].Pattern != "*" || rows[0].OwnerHandle != "alice" || rows[0].OwnerType != "user" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	// Row 1: pattern "*", handle "kmcd/platform", team
	if rows[1].Pattern != "*" || rows[1].OwnerHandle != "kmcd/platform" || rows[1].OwnerType != "team" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	// Row 2: pattern "/web/", handle "bob", user
	if rows[2].Pattern != "/web/" || rows[2].OwnerHandle != "bob" || rows[2].OwnerType != "user" {
		t.Errorf("row 2 = %+v", rows[2])
	}
	// Row 3: pattern "/api/", handle "carol" (inline comment stripped)
	if rows[3].Pattern != "/api/" || rows[3].OwnerHandle != "carol" || rows[3].OwnerType != "user" {
		t.Errorf("row 3 = %+v", rows[3])
	}
}

func TestParseCodeownersSkipsCommentsAndBlanks(t *testing.T) {
	body := "\n# top\n\n# another\n"
	if rows := parseCodeowners("r", body); len(rows) != 0 {
		t.Errorf("expected no rows, got %+v", rows)
	}
}

func TestParseCodeownersStripsAtSign(t *testing.T) {
	rows := parseCodeowners("r", "*  @alice\n")
	if len(rows) != 1 || rows[0].OwnerHandle != "alice" {
		t.Errorf("got %+v", rows)
	}
}
