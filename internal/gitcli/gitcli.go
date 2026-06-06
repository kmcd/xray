package gitcli

import (
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

	args := []string{
		"log",
		"--no-color",
		"--numstat",
		"--name-status",
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
// both --numstat and --name-status. git emits both blocks for each commit:
// numstat first ("adds\tdels\tpath"), then name-status ("A\tpath" or
// "R100\told\tnew"). We merge them by path so renames carry prev_path.
func parseFiles(s string) []FileChange {
	lines := strings.Split(s, "\n")
	type entry struct {
		add, del            int
		gotNumstat          bool
		change, path, prev  string
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
		parts := strings.Split(line, "\t")
		// numstat: "<adds>\t<dels>\t<path>" or 4 parts for renames
		// ("<adds>\t<dels>\t<old>\t<new>" in some git versions; modern
		// emits "<adds>\t<dels>\t<old> => <new>" or a curly-brace form).
		if len(parts) >= 3 && isNumOrDash(parts[0]) && isNumOrDash(parts[1]) {
			adds, _ := strconv.Atoi(parts[0])
			dels, _ := strconv.Atoi(parts[1])
			// path is the last field; renames may span multiple cols.
			path := parts[len(parts)-1]
			e := getOrCreate(path)
			e.add, e.del = adds, dels
			e.gotNumstat = true
			continue
		}
		// name-status: first field is A/M/D and friends or Rxxx/Cxxx.
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
