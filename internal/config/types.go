package config

import "time"

// Config is the parsed TOML configuration.
type Config struct {
	Window                Window
	Teams                 map[string][]string
	CaptureHarnessContent bool
	Connectors            Connectors
}

// Window is the inclusive UTC extraction window.
type Window struct {
	Start time.Time
	End   time.Time
	Raw   string
}

// Connectors holds the optional per-source config. A nil pointer means the
// connector is not configured and is skipped at extract time.
type Connectors struct {
	GitHub        *GitHubConn
	GitHubActions *GitHubActionsConn
	CircleCI      *CircleCIConn
	Sentry        *SentryConn
	Bugsnag       *BugsnagConn
	Honeycomb     *HoneycombConn
}

type GitHubConn struct {
	Token    string
	PRWindow *Window // nil → use global window for PR cluster extraction
}

type GitHubActionsConn struct {
	Token string // optional; falls back to GitHub.Token
}

type CircleCIConn struct {
	Token    string
	Projects map[string]string // circleci project slug -> repo slug
}

type SentryConn struct {
	Token        string
	Organization string
	Projects     map[string]string // sentry project slug -> repo slug
}

type BugsnagConn struct {
	Token         string
	Projects      map[string]string // bugsnag project slug -> repo slug
	MaxWindowDays int               // 0 → connector default (60d)
}

type HoneycombConn struct {
	Token   string
	Dataset string
}

// RepoToTeam returns the team a given repo slug belongs to. Empty string if
// the repo is not present in any team.
func (c *Config) RepoToTeam(slug string) string {
	for team, repos := range c.Teams {
		for _, r := range repos {
			if r == slug {
				return team
			}
		}
	}
	return ""
}

// GitHubToken returns the configured GitHub token, or "" when no GitHub
// connector is configured. Used by gitcli to authenticate clone and
// ls-remote against github.com without requiring ambient git auth.
func (c *Config) GitHubToken() string {
	if c.Connectors.GitHub == nil {
		return ""
	}
	return c.Connectors.GitHub.Token
}

// AllRepos flattens Teams into a deduplicated slice of repo slugs.
func (c *Config) AllRepos() []string {
	seen := map[string]bool{}
	var out []string
	for _, repos := range c.Teams {
		for _, r := range repos {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}
