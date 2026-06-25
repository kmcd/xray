package config

import (
	"fmt"
	"strconv"
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
	Token           string `toml:"token"`
	PROrder         string `toml:"pull_request_order"`
	PRWindow        string `toml:"pr_window"`
	PRInflection    string `toml:"pr_inflection"`
	PRBracketWindow string `toml:"pr_bracket_window"`
	PRHistorySample string `toml:"pr_history_sample"`

	IssueBugLabels        []string          `toml:"issue_bug_labels"`
	IssueRegressionLabels []string          `toml:"issue_regression_labels"`
	IssueSeverityLabels   map[string]string `toml:"issue_severity_labels"`
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
	Token         string            `toml:"token"`
	Projects      map[string]string `toml:"projects"`
	MaxWindowDays int               `toml:"max_window_days"`
}

type rawHoneycomb struct {
	Token       string `toml:"token"`
	Dataset     string `toml:"dataset"`
	Environment string `toml:"environment"`
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
		conn, err := parseGitHubConn(rc)
		if err != nil {
			return nil, nil, err
		}
		cfg.Connectors.GitHub = conn
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
			Token:         rc.Token,
			Projects:      rc.Projects,
			MaxWindowDays: rc.MaxWindowDays,
		}
	}
	if rc := raw.Connectors.Honeycomb; rc != nil {
		cfg.Connectors.Honeycomb = &HoneycombConn{
			Token:       rc.Token,
			Dataset:     rc.Dataset,
			Environment: rc.Environment,
		}
	}

	return cfg, &meta, nil
}

// parseGitHubConn converts a rawGitHub struct into a GitHubConn, parsing all
// optional PR-sampling fields. Returns an error if any field is malformed.
func parseGitHubConn(rc *rawGitHub) (*GitHubConn, error) {
	conn := &GitHubConn{Token: rc.Token}
	if rc.PROrder != "" {
		if rc.PROrder != "updated_desc" && rc.PROrder != "created_asc" {
			return nil, fmt.Errorf("connectors.github.pull_request_order: unknown value %q: must be \"updated_desc\" or \"created_asc\"", rc.PROrder)
		}
		conn.PROrder = rc.PROrder
	}
	if rc.PRWindow != "" {
		w, err := parseWindow(rc.PRWindow)
		if err != nil {
			return nil, fmt.Errorf("connectors.github.pr_window: %w", err)
		}
		conn.PRWindow = &w
	}
	if rc.PRInflection != "" {
		t, err := parseInflectionDate(rc.PRInflection)
		if err != nil {
			return nil, fmt.Errorf("connectors.github.pr_inflection: %w", err)
		}
		conn.PRInflection = &t
	}
	if rc.PRBracketWindow != "" {
		d, err := parseDurationSpec(rc.PRBracketWindow)
		if err != nil {
			return nil, fmt.Errorf("connectors.github.pr_bracket_window: %w", err)
		}
		conn.PRBracketWindow = &d
	}
	if rc.PRHistorySample != "" {
		s, err := parseHistorySample(rc.PRHistorySample)
		if err != nil {
			return nil, fmt.Errorf("connectors.github.pr_history_sample: %w", err)
		}
		conn.PRHistorySample = &s
	}
	// Issue label-sets are plain strings/maps: echo them through verbatim.
	// Defaults and case-folding are applied connector-side in github.New().
	conn.IssueBugLabels = rc.IssueBugLabels
	conn.IssueRegressionLabels = rc.IssueRegressionLabels
	conn.IssueSeverityLabels = rc.IssueSeverityLabels
	return conn, nil
}

// parseInflectionDate accepts "YYYY-MM-DD" and returns a UTC midnight time.
func parseInflectionDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD, got %q: %w", s, err)
	}
	return t, nil
}

// parseDurationSpec accepts Nu where u ∈ {y,m,w,d} and returns a DurationSpec.
// Weeks are converted to days (1w = 7d). Only a single unit per value is
// supported; compound durations ("1y6m") are not accepted.
func parseDurationSpec(s string) (DurationSpec, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return DurationSpec{}, fmt.Errorf("expected Nu (e.g. 12m), got %q", s)
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return DurationSpec{}, fmt.Errorf("expected positive integer before unit in %q", s)
	}
	d := DurationSpec{Raw: s}
	switch unit {
	case 'y':
		d.Years = n
	case 'm':
		d.Months = n
	case 'w':
		d.Days = n * 7
	case 'd':
		d.Days = n
	default:
		return DurationSpec{}, fmt.Errorf("unknown unit %q in %q: expected y, m, w, or d", string(unit), s)
	}
	return d, nil
}

// parseHistorySample accepts "monthly:N" or "monthly:N:random" and returns a
// HistorySampleSpec. N must be in [1, 100]. Other strategies are rejected
// (only "monthly" is supported in v1).
func parseHistorySample(s string) (HistorySampleSpec, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return HistorySampleSpec{}, fmt.Errorf("expected monthly:N or monthly:N:random, got %q", s)
	}
	if parts[0] != "monthly" {
		return HistorySampleSpec{}, fmt.Errorf("unknown strategy %q in %q: only \"monthly\" is supported", parts[0], s)
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 1 || n > 100 {
		return HistorySampleSpec{}, fmt.Errorf("n in %q must be in [1, 100]", s)
	}
	spec := HistorySampleSpec{Strategy: "monthly", N: n, Raw: s}
	if len(parts) == 3 {
		if parts[2] != "random" {
			return HistorySampleSpec{}, fmt.Errorf("unknown modifier %q in %q: expected \"random\"", parts[2], s)
		}
		spec.Random = true
	}
	return spec, nil
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
