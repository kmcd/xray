package gitcli

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Mailmap resolves alias (name, email) pairs to canonical (name, email).
// It mirrors git's resolution semantics as implemented by
// `git check-mailmap`: the four supported line shapes are
//
//	Proper Name <commit@email.xx>
//	<proper@email.xx> <commit@email.xx>
//	Proper Name <proper@email.xx> <commit@email.xx>
//	Proper Name <proper@email.xx> Commit Name <commit@email.xx>
//
// Comment ('#') and blank lines are skipped. Lookup keys are the (name, email)
// pair from the commit side; entries that didn't supply a commit-name match
// any commit-name with the same commit-email.
//
// A zero-value Mailmap returns inputs unchanged from Resolve and reports
// Applied() == false — the run.go aggregator uses Applied() to populate
// manifest.mailmap_applied.
type Mailmap struct {
	byPair  map[mailmapKey]mailmapVal
	byEmail map[string]mailmapVal
	loaded  bool
}

type mailmapKey struct {
	name  string
	email string
}

type mailmapVal struct {
	name  string
	email string
}

// Applied reports whether a non-empty .mailmap was parsed into this Mailmap.
// An empty file (no entries) reads as false: there's nothing to canonicalise
// and assay's mailmap_applied flag should reflect that.
func (m *Mailmap) Applied() bool {
	if m == nil {
		return false
	}
	return m.loaded && (len(m.byPair) > 0 || len(m.byEmail) > 0)
}

// Resolve returns the canonical (name, email) for a commit identity. The
// match preference matches git: pair match (commit-name + commit-email)
// beats email-only match.
func (m *Mailmap) Resolve(name, email string) (string, string) {
	if m == nil || !m.loaded {
		return name, email
	}
	key := mailmapKey{name: name, email: strings.ToLower(email)}
	if v, ok := m.byPair[key]; ok {
		return chooseField(v.name, name), chooseField(v.email, email)
	}
	if v, ok := m.byEmail[strings.ToLower(email)]; ok {
		return chooseField(v.name, name), chooseField(v.email, email)
	}
	return name, email
}

func chooseField(canon, fallback string) string {
	if canon == "" {
		return fallback
	}
	return canon
}

// ParseMailmap parses raw .mailmap bytes into a Mailmap. A zero-entry file
// (only comments / blanks) returns an empty, non-applied Mailmap.
func ParseMailmap(raw []byte) (*Mailmap, error) {
	m := &Mailmap{
		byPair:  map[mailmapKey]mailmapVal{},
		byEmail: map[string]mailmapVal{},
		loaded:  true,
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := scanner.Text()
		// Strip inline '#' comments while respecting that '#' can appear in
		// the middle of a quoted name (rare; .mailmap doesn't define quoting).
		// The reference implementation cuts on the first unescaped '#'.
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		canonName, canonEmail, commitName, commitEmail, ok := parseMailmapLine(line)
		if !ok {
			continue
		}
		val := mailmapVal{name: canonName, email: canonEmail}
		if commitName == "" {
			m.byEmail[strings.ToLower(commitEmail)] = val
			continue
		}
		m.byPair[mailmapKey{name: commitName, email: strings.ToLower(commitEmail)}] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

// parseMailmapLine extracts the four mailmap variants. Returns
// (canonName, canonEmail, commitName, commitEmail, ok). commitName is empty
// when the line was an email-only key form.
func parseMailmapLine(line string) (string, string, string, string, bool) {
	// Split into the angle-bracket segments.
	emails := []string{}
	bracketStarts := []int{}
	for i := 0; i < len(line); i++ {
		if line[i] == '<' {
			j := strings.IndexByte(line[i:], '>')
			if j < 0 {
				return "", "", "", "", false
			}
			emails = append(emails, line[i+1:i+j])
			bracketStarts = append(bracketStarts, i)
			i += j
		}
	}
	switch len(emails) {
	case 1:
		// "Proper Name <commit@email>"
		name := strings.TrimSpace(line[:bracketStarts[0]])
		if name == "" {
			return "", "", "", "", false
		}
		return name, "", "", emails[0], true
	case 2:
		left := strings.TrimSpace(line[:bracketStarts[0]])
		mid := strings.TrimSpace(line[bracketStarts[0]+len(emails[0])+2 : bracketStarts[1]])
		if left == "" && mid == "" {
			// "<proper@email> <commit@email>"
			return "", emails[0], "", emails[1], true
		}
		if left != "" && mid == "" {
			// "Proper Name <proper@email> <commit@email>"
			return left, emails[0], "", emails[1], true
		}
		if left != "" && mid != "" {
			// "Proper Name <proper@email> Commit Name <commit@email>"
			return left, emails[0], mid, emails[1], true
		}
		return "", "", "", "", false
	default:
		return "", "", "", "", false
	}
}

// ReadMailmap reads the repo's top-level .mailmap (the only location git
// supports without explicit config), returning ParseMailmap'd state. Missing
// file returns a zero-value Mailmap with Applied() == false and nil error;
// any other read or parse error is returned.
func (c *Client) ReadMailmap(_ context.Context, clonePath string) (*Mailmap, error) {
	if clonePath == "" {
		return &Mailmap{}, nil
	}
	// #nosec G304 -- clonePath is per-run temp dir under our control.
	raw, err := os.ReadFile(filepath.Join(clonePath, ".mailmap"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Mailmap{}, nil
		}
		return nil, err
	}
	return ParseMailmap(raw)
}

// CheckMailmap shells to `git check-mailmap` to resolve a single identity.
// Used by the test fixture to validate parser output against git itself; not
// called on the production hot path because parsing per repo and resolving in
// memory is orders of magnitude cheaper than forking per commit.
func (c *Client) CheckMailmap(ctx context.Context, clonePath, name, email string) (string, string, error) {
	input := name + " <" + email + ">"
	// #nosec G204 -- c.bin() is the system git binary; args are argv.
	cmd := exec.CommandContext(ctx, c.bin(), "check-mailmap", input)
	cmd.Dir = clonePath
	out, err := cmd.Output()
	if err != nil {
		return name, email, err
	}
	got := strings.TrimSpace(string(out))
	lt := strings.LastIndex(got, "<")
	gt := strings.LastIndex(got, ">")
	if lt < 0 || gt < 0 || gt < lt {
		return name, email, nil
	}
	return strings.TrimSpace(got[:lt]), got[lt+1 : gt], nil
}
