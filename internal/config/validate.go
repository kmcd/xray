package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Diagnostic is a single validation problem with a source location.
type Diagnostic struct {
	File string
	Line int
	Path string
	Msg  string
}

// Error renders the diagnostic in the spec format:
//
//	<file>:<line>: <path>: <msg>
func (d Diagnostic) Error() string {
	return fmt.Sprintf("%s:%d: %s: %s", d.File, d.Line, d.Path, d.Msg)
}

// repoSlugRe matches owner/repo. Permissive on the legal characters GitHub
// allows: alphanumerics, hyphen, underscore, dot.
// Owners cannot start with `.` or `-`. Repo names CAN start with `.` —
// the canonical example is the `<org>/.github` org-config repo, which
// GitHub treats as a real, listable repo. Repo names also accept leading
// `_` (e.g. `_redirects`-style artifacts in some toolchains).
var repoSlugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._][A-Za-z0-9._-]*$`)

// Validate enforces the schema rules from docs/spec.md and returns one
// diagnostic per problem found. file is the source path used in diagnostic
// output.
//
// Diagnostics are returned in source-line order so that the cli output is
// stable. An empty slice means the config is valid.
func Validate(cfg *Config, meta *toml.MetaData, file string) []Diagnostic {
	lines := indexLines(file)
	var out []Diagnostic

	emit := func(path, msg string) {
		out = append(out, Diagnostic{
			File: file,
			Line: lookupLine(lines, path),
			Path: path,
			Msg:  msg,
		})
	}

	// window
	if cfg.Window.Raw == "" {
		emit("window", `missing required key "window"`)
	} else if cfg.Window.End.Before(cfg.Window.Start) {
		emit("window", "end date precedes start date")
	}

	// teams: at least one team with at least one repo
	if len(cfg.Teams) == 0 {
		emit("teams", `missing required section "[teams]"`)
	} else {
		anyRepo := false
		// Sort for deterministic diagnostic order.
		names := make([]string, 0, len(cfg.Teams))
		for n := range cfg.Teams {
			names = append(names, n)
		}
		sort.Strings(names)

		// First check team-name shape and per-team repo presence.
		for _, name := range names {
			tPath := "teams." + name
			if hasWhitespace(name) {
				emit(tPath, fmt.Sprintf("team name %q must not contain whitespace", name))
			}
			repos := cfg.Teams[name]
			if len(repos) == 0 {
				emit(tPath, "team has no repos")
				continue
			}
			anyRepo = true
			for _, r := range repos {
				if !repoSlugRe.MatchString(r) {
					emit(tPath, fmt.Sprintf("repo %q is not a valid owner/repo slug", r))
				}
			}
		}
		if !anyRepo {
			emit("teams", "at least one team must contain at least one repo")
		}

		// Cross-team duplicate check: walk teams in name order, repos in their
		// declared order, and on the second sighting emit against the later
		// team with a reference to the prior one.
		owner := map[string]string{}
		for _, name := range names {
			for _, r := range cfg.Teams[name] {
				if prior, ok := owner[r]; ok && prior != name {
					emit("teams."+name,
						fmt.Sprintf("repo %q already appears in team %q", r, prior))
					continue
				}
				if _, ok := owner[r]; !ok {
					owner[r] = name
				}
			}
		}
	}

	// connectors
	c := cfg.Connectors

	// Build the set of repos declared across all teams for project-map cross-checks.
	teamRepos := map[string]bool{}
	for _, repos := range cfg.Teams {
		for _, r := range repos {
			teamRepos[r] = true
		}
	}

	// github: token is the sole required field. An empty token means the block
	// is pre-staged (all-empty); no diagnostic is emitted in that case.

	if c.GitHubActions != nil {
		if c.GitHub == nil {
			emit("connectors.github_actions",
				`requires [connectors.github] to be configured`)
		} else if c.GitHubActions.Token == "" && c.GitHub.Token == "" {
			// Token would be inherited from [connectors.github], but that
			// table is also missing a token.
			emit("connectors.github_actions",
				`missing token (and no token to inherit from [connectors.github])`)
		}
	}

	if c.CircleCI != nil {
		validateCircleCI(emit, c.CircleCI, teamRepos)
	}
	if c.Sentry != nil {
		validateSentry(emit, c.Sentry, teamRepos)
	}
	if c.Bugsnag != nil {
		validateBugsnag(emit, c.Bugsnag, teamRepos)
	}
	if c.Honeycomb != nil {
		validateHoneycomb(emit, c.Honeycomb)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Msg < out[j].Msg
	})
	return out
}

func validateCircleCI(emit func(string, string), c *CircleCIConn, teamRepos map[string]bool) {
	if c.Token == "" && len(c.Projects) == 0 {
		return
	}
	if c.Token == "" {
		emit("connectors.circleci.token", `required when [connectors.circleci] is present`)
	}
	if len(c.Projects) == 0 {
		emit("connectors.circleci.projects", `required when [connectors.circleci] is present`)
		return
	}
	validateProjectMap(emit, "connectors.circleci.projects", c.Projects, teamRepos)
}

func validateSentry(emit func(string, string), s *SentryConn, teamRepos map[string]bool) {
	if s.Token == "" && s.Organization == "" && len(s.Projects) == 0 {
		return
	}
	if s.Token == "" {
		emit("connectors.sentry.token", `required when [connectors.sentry] is present`)
	}
	if s.Organization == "" {
		emit("connectors.sentry.organization", `required when [connectors.sentry] is present`)
	}
	if len(s.Projects) == 0 {
		emit("connectors.sentry.projects", `required when [connectors.sentry] is present`)
		return
	}
	validateProjectMap(emit, "connectors.sentry.projects", s.Projects, teamRepos)
}

func validateBugsnag(emit func(string, string), b *BugsnagConn, teamRepos map[string]bool) {
	if b.Token == "" && len(b.Projects) == 0 {
		return
	}
	if b.Token == "" {
		emit("connectors.bugsnag.token", `required when [connectors.bugsnag] is present`)
	}
	if b.MaxWindowDays < 0 {
		emit("connectors.bugsnag.max_window_days", `must be a positive integer or 0 (use connector default)`)
	}
	if len(b.Projects) == 0 {
		emit("connectors.bugsnag.projects", `required when [connectors.bugsnag] is present`)
		return
	}
	validateProjectMap(emit, "connectors.bugsnag.projects", b.Projects, teamRepos)
}

func validateHoneycomb(emit func(string, string), h *HoneycombConn) {
	if h.Token == "" && h.Dataset == "" {
		return
	}
	if h.Token == "" {
		emit("connectors.honeycomb.token", `required when [connectors.honeycomb] is present`)
	}
	if h.Dataset == "" {
		emit("connectors.honeycomb.dataset", `required when [connectors.honeycomb] is present`)
	}
}

func validateProjectMap(emit func(string, string), path string, projects map[string]string, teamRepos map[string]bool) {
	keys := make([]string, 0, len(projects))
	for k := range projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := projects[k]
		if !repoSlugRe.MatchString(v) {
			emit(path, fmt.Sprintf("value %q for key %q is not a valid owner/repo slug", v, k))
		} else if len(teamRepos) > 0 && !teamRepos[v] {
			emit(path, fmt.Sprintf("value %q for key %q does not match any repo in [teams]", v, k))
		}
	}
}

func hasWhitespace(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// indexLines reads the file and produces a path->line map. Paths are the
// dotted TOML key shape, e.g. "connectors.bugsnag" or "teams.platform".
//
// This is a best-effort scanner; it covers the cases the validator cares
// about (table headers and top-level keys). When a key cannot be located
// the lookup falls back to line 1 so diagnostics still anchor somewhere
// visible.
func indexLines(path string) map[string]int {
	idx := map[string]int{}
	// #nosec G304 -- path is the config file the user just told us to validate.
	f, err := os.Open(path)
	if err != nil {
		return idx
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var currentTable string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Strip inline comments and the brackets.
			end := strings.Index(line, "]")
			if end < 0 {
				continue
			}
			header := strings.TrimSpace(line[1:end])
			// Treat [[arrays]] like [tables] for our purposes.
			header = strings.TrimPrefix(header, "[")
			header = strings.TrimSuffix(header, "]")
			header = strings.TrimSpace(header)
			currentTable = header
			if _, exists := idx[header]; !exists {
				idx[header] = lineNo
			}
			continue
		}
		// key = value
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		key = strings.Trim(key, `"'`)
		full := key
		if currentTable != "" {
			full = currentTable + "." + key
		}
		if _, exists := idx[full]; !exists {
			idx[full] = lineNo
		}
	}
	return idx
}

// lookupLine finds the best line number for a dotted path. It tries the
// exact path first, then progressively shorter prefixes (so a diagnostic
// scoped to a missing key on a table falls back to the table header line).
// Returns 1 if no anchor can be found.
func lookupLine(idx map[string]int, path string) int {
	if ln, ok := idx[path]; ok {
		return ln
	}
	parts := strings.Split(path, ".")
	for i := len(parts) - 1; i > 0; i-- {
		prefix := strings.Join(parts[:i], ".")
		if ln, ok := idx[prefix]; ok {
			return ln
		}
	}
	return 1
}
