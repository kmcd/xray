package gitcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMailmap_FourShapes(t *testing.T) {
	raw := []byte(`# Comment line — ignored.

Proper Name <commit@email.xx>
<proper@email.xx> <commit2@email.xx>
Proper Name <proper@email.xx> <commit3@email.xx>
Proper Name <proper@email.xx> Commit Name <commit4@email.xx>
`)
	mm, err := ParseMailmap(raw)
	if err != nil {
		t.Fatalf("ParseMailmap: %v", err)
	}
	if !mm.Applied() {
		t.Fatal("Applied() = false, want true")
	}

	cases := []struct {
		inName, inEmail string
		wantName        string
		wantEmail       string
	}{
		// shape 1: name-only canonicalisation by commit-email key
		{"anything", "commit@email.xx", "Proper Name", "commit@email.xx"},
		// shape 2: email-only canonicalisation; name passes through
		{"alias", "commit2@email.xx", "alias", "proper@email.xx"},
		// shape 3: name+email canonicalisation by commit-email key
		{"alias", "commit3@email.xx", "Proper Name", "proper@email.xx"},
		// shape 4: pair-keyed canonicalisation (commit name + commit email)
		{"Commit Name", "commit4@email.xx", "Proper Name", "proper@email.xx"},
		// shape 4 NEGATIVE: same commit-email but wrong commit-name -> no match
		{"Other Name", "commit4@email.xx", "Other Name", "commit4@email.xx"},
	}
	for _, tc := range cases {
		gotName, gotEmail := mm.Resolve(tc.inName, tc.inEmail)
		if gotName != tc.wantName || gotEmail != tc.wantEmail {
			t.Errorf("Resolve(%q, %q) = (%q, %q), want (%q, %q)",
				tc.inName, tc.inEmail, gotName, gotEmail, tc.wantName, tc.wantEmail)
		}
	}
}

func TestParseMailmap_EmptyAndCommentsOnly(t *testing.T) {
	mm, err := ParseMailmap([]byte("\n# only a comment\n\n"))
	if err != nil {
		t.Fatalf("ParseMailmap: %v", err)
	}
	if mm.Applied() {
		t.Errorf("Applied() = true for entry-less mailmap, want false")
	}
}

func TestMailmap_NilResolve(t *testing.T) {
	var mm *Mailmap
	if mm.Applied() {
		t.Errorf("(*Mailmap)(nil).Applied() = true, want false")
	}
	gotName, gotEmail := mm.Resolve("alice", "alice@x")
	if gotName != "alice" || gotEmail != "alice@x" {
		t.Errorf("nil Resolve = (%q, %q), want passthrough", gotName, gotEmail)
	}
}

func TestReadMailmap_MissingFile(t *testing.T) {
	dir := t.TempDir()
	c := &Client{}
	mm, err := c.ReadMailmap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadMailmap: %v", err)
	}
	if mm.Applied() {
		t.Errorf("expected Applied()=false for missing .mailmap")
	}
}

func TestReadMailmap_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	content := "Proper Name <proper@example.com> <alias@example.com>\n"
	if err := os.WriteFile(filepath.Join(dir, ".mailmap"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Client{}
	mm, err := c.ReadMailmap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadMailmap: %v", err)
	}
	if !mm.Applied() {
		t.Fatal("Applied() = false")
	}
	gotName, gotEmail := mm.Resolve("anyone", "alias@example.com")
	if gotName != "Proper Name" || gotEmail != "proper@example.com" {
		t.Errorf("Resolve = (%q, %q)", gotName, gotEmail)
	}
}

// TestMailmap_SmokeAgainstCheckMailmap matches the assay v1.1 prompt's
// acceptance smoke: three commits authored as
// Alice <alice@old>, Alice <alice@new>, Alice <alice@old> plus a
// `.mailmap` line collapsing new to old must produce one canonical author
// across all three commits — both via our pure-Go parser and via git's
// own check-mailmap (the reference implementation).
func TestMailmap_SmokeAgainstCheckMailmap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()

	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		)
		cmd.Env = append(cmd.Env, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
		}
	}

	run(nil, "init", "-b", "main")
	run(nil, "config", "commit.gpgsign", "false")

	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(".mailmap", "Alice <alice@new.example.com> <alice@old.example.com>\n")
	run(nil, "add", ".mailmap")
	run([]string{
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@old.example.com",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@old.example.com",
		"GIT_AUTHOR_DATE=2025-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2025-01-01T00:00:00Z",
	}, "commit", "-m", "first")

	write("a", "1")
	run(nil, "add", "a")
	run([]string{
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@new.example.com",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@new.example.com",
		"GIT_AUTHOR_DATE=2025-01-02T00:00:00Z", "GIT_COMMITTER_DATE=2025-01-02T00:00:00Z",
	}, "commit", "-m", "second")

	write("b", "2")
	run(nil, "add", "b")
	run([]string{
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@old.example.com",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@old.example.com",
		"GIT_AUTHOR_DATE=2025-01-03T00:00:00Z", "GIT_COMMITTER_DATE=2025-01-03T00:00:00Z",
	}, "commit", "-m", "third")

	c := &Client{}
	ctx := context.Background()
	mm, err := c.ReadMailmap(ctx, dir)
	if err != nil {
		t.Fatalf("ReadMailmap: %v", err)
	}
	if !mm.Applied() {
		t.Fatal("Applied()=false")
	}

	// Both old- and new-email identities must canonicalise to the new-email
	// per the mailmap. assay's truck-factor counts distinct authors; this
	// is the precise invariant that collapses to "one author" across all
	// three commits.
	cases := []struct{ name, email string }{
		{"Alice", "alice@old.example.com"},
		{"Alice", "alice@new.example.com"},
	}
	var resolvedEmails []string
	for _, ident := range cases {
		gotName, gotEmail := mm.Resolve(ident.name, ident.email)
		if gotName != "Alice" {
			t.Errorf("name = %q, want Alice", gotName)
		}
		resolvedEmails = append(resolvedEmails, gotEmail)
		// Cross-check against git's reference implementation.
		gitName, gitEmail, gerr := c.CheckMailmap(ctx, dir, ident.name, ident.email)
		if gerr != nil {
			t.Fatalf("CheckMailmap: %v", gerr)
		}
		if gitName != gotName || gitEmail != gotEmail {
			t.Errorf("parser vs git divergence: parser=(%q,%q) git=(%q,%q)",
				gotName, gotEmail, gitName, gitEmail)
		}
	}
	if resolvedEmails[0] != resolvedEmails[1] {
		t.Errorf("aliases failed to collapse: %q vs %q", resolvedEmails[0], resolvedEmails[1])
	}
}
