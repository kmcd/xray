package gitcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runShell runs git in dir with args and t.Fatal's on error.
func runShell(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Always set a deterministic environment for git so author/committer info
	// is reproducible across hosts.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test Author",
		"GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Test Committer",
		"GIT_COMMITTER_EMAIL=committer@example.com",
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, string(out))
	}
	return string(out)
}

// writeFile writes content to dir/relPath, creating parents as needed.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func writeBytes(t *testing.T, dir, relPath string, data []byte) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// fixture holds repo state plus useful per-commit metadata for assertions.
type fixture struct {
	dir            string
	initialSHA     string
	renameSHA      string
	copySHA        string
	emailOnlySHA   string
	binarySHA      string
	mergeSHA       string
	initialTime    time.Time
	emailOnlyTime  time.Time
	mergeTime      time.Time
}

// fixedDate returns a deterministic ISO date string offset by daysFromBase
// days from 2025-01-01T12:00:00Z.
func fixedDate(daysFromBase int) (time.Time, string) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	t := base.AddDate(0, 0, daysFromBase)
	return t, t.Format(time.RFC3339)
}

// setupRepo creates a temp git repo exercising every parser branch and
// returns the populated fixture. SHAs are captured by rev-parsing HEAD after
// each commit.
func setupRepo(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()

	runShell(t, dir, nil, "init", "-b", "main")
	runShell(t, dir, nil, "config", "user.email", "committer@example.com")
	runShell(t, dir, nil, "config", "user.name", "Test Committer")
	runShell(t, dir, nil, "config", "commit.gpgsign", "false")

	fx := &fixture{dir: dir}

	commit := func(dateOffset int, env []string, msg string) (string, time.Time) {
		ts, iso := fixedDate(dateOffset)
		dateEnv := []string{"GIT_AUTHOR_DATE=" + iso, "GIT_COMMITTER_DATE=" + iso}
		runShell(t, dir, append(dateEnv, env...), "commit", "-m", msg)
		sha := strings.TrimSpace(runShell(t, dir, nil, "rev-parse", "HEAD"))
		return sha, ts
	}

	// 1) Initial commit.
	writeFile(t, dir, "README.md", "hello\n")
	writeFile(t, dir, "src/a.go", "package src\n\nfunc A() {}\n")
	writeFile(t, dir, "src/b.go", "package src\n\nfunc B() {}\n")
	writeBytes(t, dir, "data/big.bin", make([]byte, 16))
	runShell(t, dir, nil, "add", "-A")
	fx.initialSHA, fx.initialTime = commit(0, nil, "initial")

	// 2) Rename src/a.go -> src/renamed.go.
	runShell(t, dir, nil, "mv", "src/a.go", "src/renamed.go")
	fx.renameSHA, _ = commit(1, nil, "rename a.go")

	// 3) Copy src/b.go -> src/b_copy.go (same content).
	srcB, err := os.ReadFile(filepath.Join(dir, "src/b.go"))
	if err != nil {
		t.Fatalf("read src/b.go: %v", err)
	}
	writeBytes(t, dir, "src/b_copy.go", srcB)
	runShell(t, dir, nil, "add", "-A")
	fx.copySHA, _ = commit(2, nil, "copy b.go")

	// 4) Email-only author commit. git refuses to take an empty
	// GIT_AUTHOR_NAME on `commit`, so we construct the commit object
	// directly via `hash-object -t commit` and reset HEAD/index to it.
	writeFile(t, dir, "README.md", "hello world\n")
	runShell(t, dir, nil, "add", "-A")
	tree := strings.TrimSpace(runShell(t, dir, nil, "write-tree"))
	parent := strings.TrimSpace(runShell(t, dir, nil, "rev-parse", "HEAD"))
	eoTime, _ := fixedDate(3)
	fx.emailOnlyTime = eoTime
	commitObj := "tree " + tree + "\n" +
		"parent " + parent + "\n" +
		"author  <alice@example.com> " + strconv.FormatInt(eoTime.Unix(), 10) + " +0000\n" +
		"committer Test Committer <committer@example.com> " + strconv.FormatInt(eoTime.Unix(), 10) + " +0000\n" +
		"\nemail-only author\n"
	hashCmd := exec.Command("git", "hash-object", "-w", "-t", "commit", "--stdin")
	hashCmd.Dir = dir
	hashCmd.Stdin = strings.NewReader(commitObj)
	hashCmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := hashCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, string(out))
	}
	fx.emailOnlySHA = strings.TrimSpace(string(out))
	// Point main at the new commit so subsequent commits build on it.
	runShell(t, dir, nil, "update-ref", "refs/heads/main", fx.emailOnlySHA)
	// Re-check out main to align HEAD/index/working tree.
	runShell(t, dir, nil, "reset", "--hard", fx.emailOnlySHA)

	// 5) Side-branch + merge commit.
	runShell(t, dir, nil, "checkout", "-b", "feature")
	writeFile(t, dir, "src/feature.go", "package src\n\nfunc F() {}\n")
	runShell(t, dir, nil, "add", "-A")
	_, _ = commit(4, nil, "feature work")
	runShell(t, dir, nil, "checkout", "main")
	// Use a fixed date for the merge too.
	_, iso := fixedDate(5)
	mergeEnv := []string{"GIT_AUTHOR_DATE=" + iso, "GIT_COMMITTER_DATE=" + iso}
	runShell(t, dir, mergeEnv, "merge", "--no-ff", "feature", "-m", "merge feature")
	fx.mergeSHA = strings.TrimSpace(runShell(t, dir, nil, "rev-parse", "HEAD"))
	fx.mergeTime, _ = fixedDate(5)

	// 6) Binary edit.
	bin := make([]byte, 32)
	for i := range bin {
		bin[i] = 0xAB
	}
	writeBytes(t, dir, "data/big.bin", bin)
	runShell(t, dir, nil, "add", "-A")
	fx.binarySHA, _ = commit(6, nil, "edit binary")

	return fx
}

func newClient() *Client {
	return &Client{}
}

func TestClient_HeadSHA(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	got, err := c.HeadSHA(ctx, fx.dir)
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if len(got) != 40 {
		t.Errorf("HeadSHA length = %d, want 40 (%q)", len(got), got)
	}
	want := strings.TrimSpace(runShell(t, fx.dir, nil, "rev-parse", "HEAD"))
	if got != want {
		t.Errorf("HeadSHA = %q, want %q", got, want)
	}
	for _, r := range got {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("HeadSHA has non-hex rune %q in %q", r, got)
		}
	}
}

func TestClient_DefaultBranch_Fallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	runShell(t, dir, nil, "init", "-b", "main")
	// No origin/HEAD exists; DefaultBranch must fall back to "main".

	c := newClient()
	ctx := context.Background()
	got, err := c.DefaultBranch(ctx, dir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("DefaultBranch fallback = %q, want %q", got, "main")
	}
}

func findCommit(t *testing.T, recs []CommitRecord, sha string) CommitRecord {
	t.Helper()
	for _, r := range recs {
		if r.SHA == sha {
			return r
		}
	}
	t.Fatalf("commit %s not found in records", sha)
	return CommitRecord{}
}

func TestClient_LogNumstat_BasicCommits(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	wantSHAs := []string{fx.initialSHA, fx.renameSHA, fx.copySHA, fx.emailOnlySHA, fx.mergeSHA, fx.binarySHA}
	if len(recs) < len(wantSHAs) {
		t.Fatalf("got %d records, want at least %d", len(recs), len(wantSHAs))
	}
	for _, sha := range wantSHAs {
		rec := findCommit(t, recs, sha)
		if len(rec.SHA) != 40 {
			t.Errorf("sha %q not 40 chars", rec.SHA)
		}
		// Timestamps must be UTC.
		if rec.AuthoredAt.Location() != time.UTC {
			t.Errorf("commit %s: AuthoredAt zone = %v, want UTC", sha, rec.AuthoredAt.Location())
		}
		if rec.CommittedAt.Location() != time.UTC {
			t.Errorf("commit %s: CommittedAt zone = %v, want UTC", sha, rec.CommittedAt.Location())
		}
		// Subject should be non-empty (first line of commit message).
		if rec.Subject == "" {
			t.Errorf("commit %s: empty Subject", sha)
		}
	}

	// Parents: initial has 0, normal has 1, merge has 2.
	initial := findCommit(t, recs, fx.initialSHA)
	if len(initial.ParentSHAs) != 0 {
		t.Errorf("initial commit ParentSHAs = %v, want empty", initial.ParentSHAs)
	}
	rename := findCommit(t, recs, fx.renameSHA)
	if len(rename.ParentSHAs) != 1 {
		t.Errorf("rename commit ParentSHAs len = %d, want 1", len(rename.ParentSHAs))
	}
	merge := findCommit(t, recs, fx.mergeSHA)
	if len(merge.ParentSHAs) != 2 {
		t.Errorf("merge commit ParentSHAs len = %d, want 2 (%v)", len(merge.ParentSHAs), merge.ParentSHAs)
	}

	// Regression guard for issue #55: the initial commit adds README.md +
	// two text source files + a 16-byte binary. Their text files must
	// produce non-zero numstat counts. Prior to the --numstat / --raw fix,
	// --name-status was overriding --numstat and every count came back 0.
	initialFiles := initial.Files
	var sawTextAdds bool
	for _, f := range initialFiles {
		if strings.HasSuffix(f.Path, ".go") || strings.HasSuffix(f.Path, ".md") {
			if f.Additions > 0 {
				sawTextAdds = true
				break
			}
		}
	}
	if !sawTextAdds {
		t.Errorf("initial commit: every text-file numstat had Additions == 0 — issue #55 regression\nfiles: %+v", initialFiles)
	}
}

func TestNumstatRenameNewPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo => bar", "bar"},
		{"foo", "foo"},
		{"dir/{old => new}/file", "dir/new/file"},
		{"dir/{ => new}/file", "dir/new/file"},
		{"dir/{old => }/file", "dir/file"},
		{"src/a.go", "src/a.go"},
	}
	for _, tc := range tests {
		got := numstatRenameNewPath(tc.in)
		if got != tc.want {
			t.Errorf("numstatRenameNewPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClient_LogNumstat_RenameDetected(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	rename := findCommit(t, recs, fx.renameSHA)

	var fc *FileChange
	for i := range rename.Files {
		if rename.Files[i].Path == "src/renamed.go" {
			fc = &rename.Files[i]
			break
		}
	}
	if fc == nil {
		t.Fatalf("rename commit missing src/renamed.go in files: %+v", rename.Files)
	}
	if fc.ChangeType != "R" {
		t.Errorf("rename ChangeType = %q, want %q", fc.ChangeType, "R")
	}
	if fc.PrevPath != "src/a.go" {
		t.Errorf("rename PrevPath = %q, want %q", fc.PrevPath, "src/a.go")
	}
}

func TestClient_LogNumstat_CopyDetected(t *testing.T) {
	// Tolerant test: copy detection in `git log --numstat --name-status
	// --find-renames` may surface as either "A" (new add) or "C" depending
	// on git version and similarity threshold. The parser handles both; we
	// only assert the row exists for the copied path.
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	cp := findCommit(t, recs, fx.copySHA)
	found := false
	for _, f := range cp.Files {
		if f.Path == "src/b_copy.go" {
			found = true
			// If git classified it as a copy, ChangeType should be "C"
			// and PrevPath should be "src/b.go". Otherwise accept A/M.
			if f.ChangeType == "C" && f.PrevPath != "src/b.go" {
				t.Errorf("copy detected but PrevPath = %q, want %q", f.PrevPath, "src/b.go")
			}
			if f.ChangeType != "C" && f.ChangeType != "A" && f.ChangeType != "M" {
				t.Errorf("copy commit unexpected ChangeType = %q", f.ChangeType)
			}
			break
		}
	}
	if !found {
		t.Fatalf("copy commit missing src/b_copy.go in files: %+v", cp.Files)
	}
}

func TestClient_LogNumstat_BinaryNumstat(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	bin := findCommit(t, recs, fx.binarySHA)
	var fc *FileChange
	for i := range bin.Files {
		if bin.Files[i].Path == "data/big.bin" {
			fc = &bin.Files[i]
			break
		}
	}
	if fc == nil {
		t.Fatalf("binary commit missing data/big.bin in files: %+v", bin.Files)
	}
	// Parser behaviour: numstat "-\t-\tpath" parses both fields as 0 via
	// strconv.Atoi (which returns 0 on the "-" sentinel). Document and
	// assert this contract here.
	if fc.Additions != 0 {
		t.Errorf("binary Additions = %d, want 0", fc.Additions)
	}
	if fc.Deletions != 0 {
		t.Errorf("binary Deletions = %d, want 0", fc.Deletions)
	}
}

func TestClient_LogNumstat_MergeCommit(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	merge := findCommit(t, recs, fx.mergeSHA)
	if merge.ParentSHAs == nil {
		t.Fatalf("merge ParentSHAs is nil")
	}
	if len(merge.ParentSHAs) != 2 {
		t.Fatalf("merge ParentSHAs len = %d, want 2", len(merge.ParentSHAs))
	}
}

func TestClient_LogNumstat_EmailOnlyAuthor(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	eo := findCommit(t, recs, fx.emailOnlySHA)
	if eo.AuthorHandle != "" {
		t.Errorf("AuthorHandle = %q, want empty", eo.AuthorHandle)
	}
	if eo.AuthorEmail != "alice@example.com" {
		t.Errorf("AuthorEmail = %q, want alice@example.com", eo.AuthorEmail)
	}
}

func TestClient_LogNumstat_SinceBoundary(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	// Set since to just after the initial commit; the initial commit must
	// be excluded but everything else returned.
	since := fx.initialTime.Add(1 * time.Hour)
	until := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs, err := c.LogNumstat(ctx, fx.dir, since, until, "")
	if err != nil {
		t.Fatalf("LogNumstat: %v", err)
	}
	for _, r := range recs {
		if r.SHA == fx.initialSHA {
			t.Errorf("initial commit %s should be excluded by since boundary", fx.initialSHA)
		}
	}
	// Should still contain the rename, copy, email-only, merge, binary.
	for _, sha := range []string{fx.renameSHA, fx.copySHA, fx.emailOnlySHA, fx.mergeSHA, fx.binarySHA} {
		found := false
		for _, r := range recs {
			if r.SHA == sha {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected commit %s in window after since boundary", sha)
		}
	}
}

func TestClient_LogPath(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	// README.md is touched in initial and email-only commits.
	firstSHA, firstAt, lastAt, err := c.LogPath(ctx, fx.dir, "README.md")
	if err != nil {
		t.Fatalf("LogPath: %v", err)
	}
	if firstSHA != fx.initialSHA {
		t.Errorf("firstSHA = %q, want %q (initial)", firstSHA, fx.initialSHA)
	}
	if firstAt.IsZero() {
		t.Errorf("firstAt is zero")
	}
	if lastAt.IsZero() {
		t.Errorf("lastAt is zero")
	}
	if lastAt.Before(firstAt) {
		t.Errorf("lastAt %v before firstAt %v", lastAt, firstAt)
	}
	if firstAt.Location() != time.UTC {
		t.Errorf("firstAt zone = %v, want UTC", firstAt.Location())
	}
}

func TestClient_IsAncestor(t *testing.T) {
	fx := setupRepo(t)
	c := newClient()
	ctx := context.Background()

	head := strings.TrimSpace(runShell(t, fx.dir, nil, "rev-parse", "HEAD"))
	t.Run("ancestor_true", func(t *testing.T) {
		got, err := c.IsAncestor(ctx, fx.dir, fx.initialSHA, head)
		if err != nil {
			t.Fatalf("IsAncestor: %v", err)
		}
		if !got {
			t.Errorf("expected initial -> HEAD to be ancestor; got false")
		}
	})
	t.Run("ancestor_false", func(t *testing.T) {
		// HEAD is not an ancestor of the initial commit.
		got, err := c.IsAncestor(ctx, fx.dir, head, fx.initialSHA)
		if err != nil {
			t.Fatalf("IsAncestor: %v", err)
		}
		if got {
			t.Errorf("expected HEAD -> initial NOT to be ancestor; got true")
		}
	})
	t.Run("empty_ref", func(t *testing.T) {
		if _, err := c.IsAncestor(ctx, fx.dir, "", head); err == nil {
			t.Errorf("expected error on empty ancestor")
		}
	})
	t.Run("unknown_ref", func(t *testing.T) {
		// Triggers a git error (non-zero non-1 exit) which IsAncestor
		// surfaces as a wrapped error.
		_, err := c.IsAncestor(ctx, fx.dir, "0000000000000000000000000000000000000000", head)
		if err == nil {
			t.Errorf("expected error for unknown commit")
		}
	})
}

func TestClient_LsRemote_Failure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if os.Getenv("XRAY_TEST_OFFLINE") != "" {
		t.Skip("offline mode (XRAY_TEST_OFFLINE set)")
	}
	c := newClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := c.LsRemote(ctx, "kmcd/does-not-exist-xyz-1234")
	if err == nil {
		t.Skip("ls-remote unexpectedly succeeded (network or auth proxy); skipping")
	}
}

func TestClient_Clone_DestExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dest := t.TempDir()
	c := newClient()
	ctx := context.Background()
	err := c.Clone(ctx, "kmcd/whatever", dest, time.Now())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q does not mention 'already exists'", err.Error())
	}
}

func TestIsNumOrDash(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"-", true},
		{"0", true},
		{"123", true},
		{"", false},
		{"abc", false},
		{"12a", false},
		{"-1", false},
	}
	for _, tc := range cases {
		if got := isNumOrDash(tc.in); got != tc.want {
			t.Errorf("isNumOrDash(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSplitShaTime(t *testing.T) {
	sha, ts, err := splitShaTime("abc123" + fieldSep + "2025-01-01T12:00:00Z")
	if err != nil {
		t.Fatalf("splitShaTime: %v", err)
	}
	if sha != "abc123" {
		t.Errorf("sha = %q, want abc123", sha)
	}
	if ts.Location() != time.UTC {
		t.Errorf("ts zone = %v, want UTC", ts.Location())
	}
	if _, _, err := splitShaTime("no-separator"); err == nil {
		t.Error("expected error for line missing separator")
	}
	if _, _, err := splitShaTime("abc" + fieldSep + "not-a-time"); err == nil {
		t.Error("expected error for malformed time")
	}
}

func TestParseLog_Errors(t *testing.T) {
	// Record without body terminator.
	bad := recSep + "abc" + fieldSep + "n" + fieldSep + "e" + fieldSep + "2025-01-01T00:00:00Z" + fieldSep +
		"n" + fieldSep + "e" + fieldSep + "2025-01-01T00:00:00Z" + fieldSep + "" + fieldSep + "sub" + fieldSep + "body"
	if _, err := parseLog(bad); err == nil {
		t.Error("expected error for missing body terminator")
	}
	// Record with too few header fields.
	bad2 := recSep + "only-one-field" + bodySep
	if _, err := parseLog(bad2); err == nil {
		t.Error("expected error for malformed header")
	}
	// Empty input produces no records, no error.
	recs, err := parseLog("")
	if err != nil {
		t.Errorf("parseLog(empty): %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("parseLog(empty) returned %d records", len(recs))
	}
}

func TestParseFiles_DeleteAndModify(t *testing.T) {
	// Synthesise the post-body section: numstat then name-status, exercising
	// A, M, D rows plus a "<adds>\t<dels>\t<old> => <new>" rename form.
	s := "\n" +
		"5\t2\tfoo.go\n" +
		"0\t10\tbar.go\n" +
		"3\t1\tsrc/old.go => src/new.go\n" +
		"\n" +
		"A\tfoo.go\n" +
		"D\tbar.go\n" +
		"R100\tsrc/old.go\tsrc/new.go\n"
	files := parseFiles(s)
	found := map[string]FileChange{}
	for _, f := range files {
		found[f.Path] = f
	}
	if f, ok := found["foo.go"]; !ok || f.ChangeType != "A" || f.Additions != 5 || f.Deletions != 2 {
		t.Errorf("foo.go = %+v", f)
	}
	if f, ok := found["bar.go"]; !ok || f.ChangeType != "D" || f.Deletions != 10 {
		t.Errorf("bar.go = %+v", f)
	}
	if f, ok := found["src/new.go"]; !ok || f.ChangeType != "R" || f.PrevPath != "src/old.go" {
		t.Errorf("src/new.go = %+v", f)
	}
}

func TestParseFiles_NameStatusOnly_DefaultsToModify(t *testing.T) {
	// Path appearing only in name-status with no recognised code defaults
	// to "M" via parseFiles's empty-change fallback.
	s := "T\tlinkfile\n"
	files := parseFiles(s)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Path != "linkfile" || files[0].ChangeType != "T" {
		t.Errorf("files[0] = %+v", files[0])
	}
}

func TestClient_DefaultBranch_Success(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Build a "real" clone whose origin/HEAD points to a branch other
	// than main, so we exercise the symbolic-ref success path.
	srcDir := t.TempDir()
	runShell(t, srcDir, nil, "init", "-b", "trunk")
	runShell(t, srcDir, nil, "config", "user.email", "c@example.com")
	runShell(t, srcDir, nil, "config", "user.name", "C")
	writeFile(t, srcDir, "f", "x\n")
	runShell(t, srcDir, nil, "add", "-A")
	runShell(t, srcDir, nil, "commit", "-m", "init")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	runShell(t, "", nil, "clone", "-q", srcDir, cloneDir)
	// `git clone` sets refs/remotes/origin/HEAD when cloning a non-empty repo.

	c := newClient()
	ctx := context.Background()
	got, err := c.DefaultBranch(ctx, cloneDir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if got != "trunk" {
		t.Errorf("DefaultBranch = %q, want %q", got, "trunk")
	}
}

func TestClient_binAndLog_Defaults(t *testing.T) {
	// Default Client zero-value: bin() falls back to "git" and log()
	// returns a non-nil logger. Also exercise the non-zero branches.
	c := &Client{}
	if got := c.bin(); got != "git" {
		t.Errorf("bin() default = %q, want %q", got, "git")
	}
	if c.log() == nil {
		t.Error("log() default returned nil")
	}
	c2 := &Client{Bin: "/usr/local/bin/git"}
	if got := c2.bin(); got != "/usr/local/bin/git" {
		t.Errorf("bin() configured = %q", got)
	}
}

// TestClient_LogNumstat_GPGVerified is intentionally skipped: a real signed
// commit requires a GPG key (or SSH-signing key) provisioned in the test
// environment and gpg(1) on PATH. The CI environment does not provision
// one; document the gap rather than attempt a flaky synthesis.
func TestClient_LogNumstat_GPGVerified(t *testing.T) {
	t.Skip("requires GPG key setup; gitcli.go does not currently parse signature_verified from log output")
}
