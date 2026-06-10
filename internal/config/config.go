package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// rawConfig mirrors the on-disk TOML shape. It is intentionally separate from
// the public Config so the wire format can evolve without touching the public
// type.
type rawConfig struct {
	Window                string              `toml:"window"`
	CaptureHarnessContent bool                `toml:"capture_harness_content"`
	Teams                 map[string][]string `toml:"teams"`
	Connectors            rawConnectors       `toml:"connectors"`
}

type rawConnectors struct {
	GitHub        *rawGitHub        `toml:"github"`
	GitHubActions *rawGitHubActions `toml:"github_actions"`
	CircleCI      *rawCircleCI      `toml:"circleci"`
	Sentry        *rawSentry        `toml:"sentry"`
	Bugsnag       *rawBugsnag       `toml:"bugsnag"`
	Honeycomb     *rawHoneycomb     `toml:"honeycomb"`
}

type rawGitHub struct {
	Token string `toml:"token"`
}

type rawGitHubActions struct {
	Token string `toml:"token"`
}

type rawCircleCI struct {
	Token    string            `toml:"token"`
	Projects map[string]string `toml:"projects"`
}

type rawSentry struct {
	Token        string            `toml:"token"`
	Organization string            `toml:"organization"`
	Projects     map[string]string `toml:"projects"`
}

type rawBugsnag struct {
	Token    string            `toml:"token"`
	Projects map[string]string `toml:"projects"`
}

type rawHoneycomb struct {
	Token   string `toml:"token"`
	Dataset string `toml:"dataset"`
}

// Load reads a TOML config file from path and returns the populated Config
// plus the toml.MetaData that produced it. The MetaData is needed by
// Validate for line-numbered diagnostics.
//
// A malformed window string is reported as an error here rather than as a
// validation diagnostic: a window we cannot parse cannot be validated.
func Load(path string) (*Config, *toml.MetaData, error) {
	var raw rawConfig
	meta, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return nil, nil, err
	}

	cfg := &Config{
		CaptureHarnessContent: raw.CaptureHarnessContent,
		Teams:                 raw.Teams,
	}

	if raw.Window != "" {
		w, err := parseWindow(raw.Window)
		if err != nil {
			return nil, nil, fmt.Errorf("window: %w", err)
		}
		cfg.Window = w
	}

	if rc := raw.Connectors.GitHub; rc != nil {
		cfg.Connectors.GitHub = &GitHubConn{Token: rc.Token}
	}
	if rc := raw.Connectors.GitHubActions; rc != nil {
		tok := rc.Token
		if tok == "" && cfg.Connectors.GitHub != nil {
			tok = cfg.Connectors.GitHub.Token
		}
		cfg.Connectors.GitHubActions = &GitHubActionsConn{Token: tok}
	}
	if rc := raw.Connectors.CircleCI; rc != nil {
		cfg.Connectors.CircleCI = &CircleCIConn{Token: rc.Token, Projects: rc.Projects}
	}
	if rc := raw.Connectors.Sentry; rc != nil {
		cfg.Connectors.Sentry = &SentryConn{
			Token:        rc.Token,
			Organization: rc.Organization,
			Projects:     rc.Projects,
		}
	}
	if rc := raw.Connectors.Bugsnag; rc != nil {
		cfg.Connectors.Bugsnag = &BugsnagConn{
			Token:    rc.Token,
			Projects: rc.Projects,
		}
	}
	if rc := raw.Connectors.Honeycomb; rc != nil {
		cfg.Connectors.Honeycomb = &HoneycombConn{
			Token:   rc.Token,
			Dataset: rc.Dataset,
		}
	}

	return cfg, &meta, nil
}

// parseWindow accepts "YYYY-MM-DD..YYYY-MM-DD" and returns a Window with
// UTC-anchored start and end. The original string is preserved verbatim in
// Window.Raw.
func parseWindow(s string) (Window, error) {
	parts := strings.SplitN(s, "..", 2)
	if len(parts) != 2 {
		return Window{}, fmt.Errorf("expected YYYY-MM-DD..YYYY-MM-DD, got %q", s)
	}
	start, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(parts[0]), time.UTC)
	if err != nil {
		return Window{}, fmt.Errorf("invalid start date %q: %w", parts[0], err)
	}
	end, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(parts[1]), time.UTC)
	if err != nil {
		return Window{}, fmt.Errorf("invalid end date %q: %w", parts[1], err)
	}
	return Window{Start: start, End: end, Raw: s}, nil
}
