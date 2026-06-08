package gitcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client is a thin wrapper around the system git binary. The GitHub token
// in xray's config is used for API access only; clone relies on the user's
// ambient git authentication (SSH, credential helper, gh CLI, etc.).
type Client struct {
	Bin string
	Log *slog.Logger
}

func (c *Client) bin() string {
	if c.Bin == "" {
		return "git"
	}
	return c.Bin
}

func (c *Client) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return c.Log
}

func (c *Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- c.bin() is the system `git` binary; args are passed as
	// argv (not through a shell) and originate from internal callers.
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	c.log().Debug("git", slog.String("dir", dir), slog.Any("args", args))
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Clone shallow-clones slug ("owner/repo") into dest. dest must not exist.
// The shallow window starts at shallowSince - 30d to keep rename history
// (commit_files prev_path tracking) coherent at the window boundary.
func (c *Client) Clone(ctx context.Context, slug, dest string, shallowSince time.Time) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("gitcli: clone dest already exists: %s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gitcli: stat dest: %w", err)
	}
	url := fmt.Sprintf("https://github.com/%s.git", slug)
	since := shallowSince.UTC().AddDate(0, 0, -30).Format("2006-01-02")
	_, err := c.run(ctx, "",
		"clone",
		"--no-tags",
		"--shallow-since="+since,
		url, dest,
	)
	return err
}

// ShowFile streams the contents of path at sha from clonePath, equivalent
// to `git show <sha>:<path>`. Returns os.ErrNotExist if the path is absent
// at that revision (typical for delete entries). Output is capped at
// maxShowFileBytes so a runaway blob can't OOM the extractor.
func (c *Client) ShowFile(ctx context.Context, clonePath, sha, path string) ([]byte, error) {
	if sha == "" || path == "" {
		return nil, fmt.Errorf("gitcli: empty sha or path")
	}
	// #nosec G204 -- c.bin() is the system git binary; args are argv.
	cmd := exec.CommandContext(ctx, c.bin(), "show", sha+":"+path)
	cmd.Dir = clonePath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// `git show <sha>:<path>` exits 128 with stderr "fatal: ...: does
		// not exist..." or "...exists on disk, but not in '<sha>'..." for
		// missing entries. Surface that as os.ErrNotExist so callers can
		// distinguish a delete from a real failure.
		serr := strings.ToLower(stderr.String())
		if strings.Contains(serr, "does not exist") || strings.Contains(serr, "exists on disk") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("git show %s:%s: %w: %s", sha, path, err, strings.TrimSpace(stderr.String()))
	}
	out := stdout.Bytes()
	if len(out) > maxShowFileBytes {
		out = out[:maxShowFileBytes]
	}
	return out, nil
}

// maxShowFileBytes caps a single blob read so per-revision walks can't OOM
// on a checked-in giant. 8 MiB is well above any indent-counted source file;
// binaries and minified bundles are filtered upstream.
const maxShowFileBytes = 8 * 1024 * 1024

// CatFileBatch calls git cat-file --batch for all refs (each "<sha>:<path>")
// and invokes fn once per ref in input order. fn receives the raw blob
// content, or nil when the object is absent or not a blob. Content per
// object is capped at maxShowFileBytes. One subprocess handles all refs.
func (c *Client) CatFileBatch(ctx context.Context, clonePath string, refs []string, fn func(ref string, content []byte)) error {
	if len(refs) == 0 {
		return nil
	}
	// #nosec G204
	cmd := exec.CommandContext(ctx, c.bin(), "cat-file", "--batch")
	cmd.Dir = clonePath
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("gitcli: cat-file --batch: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gitcli: cat-file --batch: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gitcli: cat-file --batch: %w", err)
	}
	// Write all queries in the background; close stdin when done so git sees EOF.
	go func() {
		for _, ref := range refs {
			fmt.Fprintln(stdin, ref)
		}
		stdin.Close()
	}()
	r := bufio.NewReader(stdout)
	for _, ref := range refs {
		line, err := r.ReadString('\n')
		if err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("gitcli: cat-file --batch: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasSuffix(line, " missing") {
			fn(ref, nil)
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			fn(ref, nil)
			continue
		}
		size, err := strconv.Atoi(parts[2])
		if err != nil || size < 0 {
			fn(ref, nil)
			continue
		}
		if parts[1] != "blob" {
			// Non-blob (tree, commit): consume bytes + LF separator, return nil.
			if _, err := io.CopyN(io.Discard, r, int64(size)+1); err != nil {
				_ = cmd.Wait()
				return fmt.Errorf("gitcli: cat-file --batch: %w", err)
			}
			fn(ref, nil)
			continue
		}
		toRead := size
		if toRead > maxShowFileBytes {
			toRead = maxShowFileBytes
		}
		content := make([]byte, toRead)
		if _, err := io.ReadFull(r, content); err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("gitcli: cat-file --batch: %w", err)
		}
		// Discard any uncapped remainder plus the LF object separator.
		if _, err := io.CopyN(io.Discard, r, int64(size-toRead)+1); err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("gitcli: cat-file --batch: %w", err)
		}
		fn(ref, content)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("gitcli: cat-file --batch: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// IsAncestor reports whether `ancestor` is an ancestor of (or equal to)
// `descendant` in clonePath. Implemented via `git merge-base --is-ancestor`,
// which exits 0 when true and 1 when false; any other exit code is surfaced
// as an error. Added to support ADR-021's merge-method derivation in the
// github connector: rebase vs squash is the reachability of the PR's head
// commits from the merge commit.
func (c *Client) IsAncestor(ctx context.Context, clonePath, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, fmt.Errorf("gitcli: empty ref")
	}
	// #nosec G204 -- c.bin() is the system git binary; args are argv.
	cmd := exec.CommandContext(ctx, c.bin(), "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = clonePath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 1:
				return false, nil
			default:
				return false, fmt.Errorf("git merge-base --is-ancestor: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
		}
		return false, fmt.Errorf("git merge-base --is-ancestor: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
}

// LsRemote verifies clone access without cloning.
func (c *Client) LsRemote(ctx context.Context, slug string) error {
	url := fmt.Sprintf("https://github.com/%s.git", slug)
	_, err := c.run(ctx, "", "ls-remote", "--exit-code", url, "HEAD")
	return err
}

// HeadSHA returns the SHA of HEAD in clonePath.
func (c *Client) HeadSHA(ctx context.Context, clonePath string) (string, error) {
	out, err := c.run(ctx, clonePath, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// DefaultBranch resolves the default branch of clonePath from
// origin/HEAD's symbolic ref. Falls back to "main" on failure.
func (c *Client) DefaultBranch(ctx context.Context, clonePath string) (string, error) {
	out, err := c.run(ctx, clonePath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "main", nil
	}
	ref := strings.TrimSpace(out)
	// refs/remotes/origin/<branch>
	const prefix = "refs/remotes/origin/"
	if strings.HasPrefix(ref, prefix) {
		return ref[len(prefix):], nil
	}
	return "main", nil
}

// CommitRecord is a parsed git log entry covering everything the github
// connector needs to populate commits, commit_files, and commit_coauthors.
// Body is exposed so the connector can parse trailers and structured
// signals; the connector discards it afterwards.
type CommitRecord struct {
	SHA             string
	AuthorHandle    string
	AuthorEmail     string
	CommitterHandle string
	CommitterEmail  string
	AuthoredAt      time.Time
	CommittedAt     time.Time
	Subject         string
	Body            string
	ParentSHAs      []string
	Files           []FileChange
}

// FileChange is a single per-file numstat row attached to a commit.
type FileChange struct {
	Path       string
	PrevPath   string
	ChangeType string // A | M | D | R | C
	Additions  int
	Deletions  int
}

// commit record delimiter; chosen to be vanishingly unlikely in commit text.
const (
	recSep   = "\x1ecommit\x1e"
	fieldSep = "\x1f"
	bodySep  = "\x1eendbody\x1e"
)

// LogNumstat streams parsed commits + per-file numstat in the window.
// branch is the ref to walk; if empty, HEAD is used.
func (c *Client) LogNumstat(ctx context.Context, clonePath string, since, until time.Time, branch string) ([]CommitRecord, error) {
	format := recSep +
		"%H" + fieldSep + // 0 sha
		"%an" + fieldSep + // 1 author name
		"%ae" + fieldSep + // 2 author email
		"%aI" + fieldSep + // 3 author date ISO strict
		"%cn" + fieldSep + // 4 committer name
		"%ce" + fieldSep + // 5 committer email
		"%cI" + fieldSep + // 6 committer date ISO strict
		"%P" + fieldSep + // 7 parents space-separated
		"%s" + fieldSep + // 8 subject
		"%b" + // 9 body (may be multiline)
		bodySep

	// --numstat gives additions/deletions; --raw gives change_type and the
	// pre-rename path. They compose; --numstat + --name-status do NOT
	// (--name-status wins and numstat is dropped silently on modern git).
	args := []string{
		"log",
		"--no-color",
		"--numstat",
		"--raw",
		"--find-renames",
		"--date=iso-strict",
		"--since=" + since.UTC().Format(time.RFC3339),
		"--until=" + until.UTC().Format(time.RFC3339),
		"--pretty=format:" + format,
	}
	if branch != "" {
		args = append(args, branch)
	}

	out, err := c.run(ctx, clonePath, args...)
	if err != nil {
		return nil, err
	}
	return parseLog(out)
}

func parseLog(out string) ([]CommitRecord, error) {
	// Records are separated by recSep. The first chunk before the first
	// recSep is empty (output starts with the delimiter).
	chunks := strings.Split(out, recSep)
	var records []CommitRecord
	for _, chunk := range chunks {
		chunk = strings.TrimLeft(chunk, "\n")
		if chunk == "" {
			continue
		}
		// Split off the body terminator.
		idx := strings.Index(chunk, bodySep)
		if idx < 0 {
			return nil, fmt.Errorf("gitcli: malformed log record (no body terminator)")
		}
		header := chunk[:idx]
		rest := chunk[idx+len(bodySep):]

		fields := strings.SplitN(header, fieldSep, 10)
		if len(fields) < 10 {
			return nil, fmt.Errorf("gitcli: malformed log header: %d fields", len(fields))
		}
		rec := CommitRecord{
			SHA:             fields[0],
			AuthorHandle:    fields[1],
			AuthorEmail:     fields[2],
			CommitterHandle: fields[4],
			CommitterEmail:  fields[5],
			Subject:         fields[8],
			Body:            fields[9],
		}
		if t, err := time.Parse(time.RFC3339, fields[3]); err == nil {
			rec.AuthoredAt = t.UTC()
		}
		if t, err := time.Parse(time.RFC3339, fields[6]); err == nil {
			rec.CommittedAt = t.UTC()
		}
		if p := strings.TrimSpace(fields[7]); p != "" {
			rec.ParentSHAs = strings.Fields(p)
		}

		// Per-file rows follow the body terminator: blank line(s) then
		// alternating numstat / name-status rows.
		rec.Files = parseFiles(rest)
		records = append(records, rec)
	}
	return records, nil
}

// parseFiles parses the post-header section of a git log entry produced with
// --numstat and --raw. git emits both blocks for each commit:
//
//   raw line:     ":<src_mode> <dst_mode> <src_sha> <dst_sha> <code>\t<path>"
//                 (rename / copy variants append \t<newpath>: code is Rnn / Cnn)
//   numstat line: "<adds>\t<dels>\t<path>" or
//                 "<adds>\t<dels>\t<oldpath> => <newpath>" for renames.
//
// We merge them by canonical (new) path so renames carry both prev_path and
// the numstat counts. Older callers used --name-status, which silently
// suppresses --numstat output on modern git; the parser is kept tolerant of
// either block being present in case future flag changes drop one.
func parseFiles(s string) []FileChange {
	lines := strings.Split(s, "\n")
	type entry struct {
		add, del           int
		gotNumstat         bool
		change, path, prev string
	}
	order := []string{}
	byPath := map[string]*entry{}
	getOrCreate := func(path string) *entry {
		if e, ok := byPath[path]; ok {
			return e
		}
		e := &entry{path: path}
		byPath[path] = e
		order = append(order, path)
		return e
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		// raw block: lines begin with ':'.
		if strings.HasPrefix(line, ":") {
			parts := strings.Split(line, "\t")
			// parts[0] = ":<mode> <mode> <sha> <sha> <code>"
			head := strings.Fields(parts[0])
			if len(head) < 5 || len(parts) < 2 {
				continue
			}
			code := head[4]
			switch {
			case code == "A", code == "M", code == "D", code == "T", code == "U":
				e := getOrCreate(parts[1])
				e.change = code
			case strings.HasPrefix(code, "R"), strings.HasPrefix(code, "C"):
				if len(parts) >= 3 {
					e := getOrCreate(parts[2])
					e.change = string(code[0])
					e.prev = parts[1]
				}
			}
			continue
		}
		parts := strings.Split(line, "\t")
		// numstat: "<adds>\t<dels>\t<path>".
		if len(parts) >= 3 && isNumOrDash(parts[0]) && isNumOrDash(parts[1]) {
			adds, _ := strconv.Atoi(parts[0])
			dels, _ := strconv.Atoi(parts[1])
			path := parts[len(parts)-1]
			// Renames in numstat use "<old> => <new>" or "<dir>/{<old> => <new>}/<rest>".
			// Reduce to the new path so the entry matches the raw block.
			path = numstatRenameNewPath(path)
			e := getOrCreate(path)
			e.add, e.del = adds, dels
			e.gotNumstat = true
			continue
		}
		// Legacy --name-status compatibility: first field is A/M/D or Rxxx/Cxxx.
		if len(parts) >= 2 {
			code := parts[0]
			switch {
			case code == "A", code == "M", code == "D", code == "T", code == "U":
				e := getOrCreate(parts[1])
				e.change = code
			case strings.HasPrefix(code, "R"), strings.HasPrefix(code, "C"):
				if len(parts) >= 3 {
					e := getOrCreate(parts[2])
					e.change = string(code[0])
					e.prev = parts[1]
				}
			}
		}
	}

	out := make([]FileChange, 0, len(order))
	for _, path := range order {
		e := byPath[path]
		change := e.change
		if change == "" {
			change = "M"
		}
		out = append(out, FileChange{
			Path:       path,
			PrevPath:   e.prev,
			ChangeType: change,
			Additions:  e.add,
			Deletions:  e.del,
		})
	}
	return out
}

// numstatRenameNewPath collapses git's numstat rename shorthand to the
// post-rename path so it matches the new path in the --raw block.
//
//   "foo => bar"              -> "bar"
//   "dir/{old => new}/file"   -> "dir/new/file"
//   "dir/{ => new}/file"      -> "dir/new/file"
//   "dir/{old => }/file"      -> "dir/file"
//
// Anything that doesn't match a rename pattern is returned unchanged.
func numstatRenameNewPath(s string) string {
	if i := strings.Index(s, "{"); i >= 0 {
		j := strings.Index(s[i:], "}")
		if j < 0 {
			return s
		}
		j += i
		inner := s[i+1 : j]
		var newPart string
		if arrow := strings.Index(inner, " => "); arrow >= 0 {
			newPart = inner[arrow+4:]
		} else {
			newPart = inner
		}
		out := s[:i] + newPart + s[j+1:]
		// Collapse the possible empty-path artifact "dir//file" -> "dir/file".
		out = strings.ReplaceAll(out, "//", "/")
		return out
	}
	if arrow := strings.Index(s, " => "); arrow >= 0 {
		return s[arrow+4:]
	}
	return s
}

func isNumOrDash(s string) bool {
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// LogPath returns the first-seen commit/time and last-modified time for a
// single path. The clone must contain enough history to reach the path's
// introducing commit; for working-tree artifacts this is satisfied by the
// shallowSince - 30d clone window.
func (c *Client) LogPath(ctx context.Context, clonePath, path string) (string, time.Time, time.Time, error) {
	out, err := c.run(ctx, clonePath,
		"log", "--no-color", "--reverse",
		"--pretty=format:%H"+fieldSep+"%cI",
		"--", path,
	)
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", time.Time{}, time.Time{}, fmt.Errorf("gitcli: no history for %s", path)
	}
	lines := strings.Split(out, "\n")
	firstSHA, firstAt, err := splitShaTime(lines[0])
	if err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	lastAt := firstAt
	if len(lines) > 1 {
		_, lastAt, err = splitShaTime(lines[len(lines)-1])
		if err != nil {
			return "", time.Time{}, time.Time{}, err
		}
	}
	return firstSHA, firstAt, lastAt, nil
}

// RemoteBranch is one row of `git for-each-ref refs/remotes/origin/`.
// Name is the short ref with the `origin/` prefix stripped.
type RemoteBranch struct {
	Name          string
	LastCommitSHA string
	LastCommitAt  time.Time
}

// RemoteBranches enumerates origin's branches from the local clone,
// returning name, tip SHA, and committer date for each. `origin/HEAD`
// is filtered out. Replaces a REST ListBranches round-trip; the data
// is already in the clone after fetch.
func (c *Client) RemoteBranches(ctx context.Context, clonePath string) ([]RemoteBranch, error) {
	format := "%(refname:short)" + fieldSep + "%(objectname)" + fieldSep + "%(committerdate:iso-strict)"
	out, err := c.run(ctx, clonePath, "for-each-ref",
		"--format="+format,
		"refs/remotes/origin/",
	)
	if err != nil {
		return nil, err
	}
	var rows []RemoteBranch
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, fieldSep, 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("gitcli: malformed for-each-ref line: %q", line)
		}
		name := strings.TrimPrefix(parts[0], "origin/")
		if name == "HEAD" {
			continue
		}
		t, err := time.Parse(time.RFC3339, parts[2])
		if err != nil {
			return nil, fmt.Errorf("gitcli: parse committerdate %q: %w", parts[2], err)
		}
		rows = append(rows, RemoteBranch{
			Name:          name,
			LastCommitSHA: parts[1],
			LastCommitAt:  t.UTC(),
		})
	}
	return rows, nil
}

func splitShaTime(line string) (string, time.Time, error) {
	parts := strings.SplitN(line, fieldSep, 2)
	if len(parts) != 2 {
		return "", time.Time{}, fmt.Errorf("gitcli: malformed sha/time line: %q", line)
	}
	t, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gitcli: parse time %q: %w", parts[1], err)
	}
	return parts[0], t.UTC(), nil
}
