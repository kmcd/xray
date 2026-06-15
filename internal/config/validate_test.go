package config

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		want    []string // substrings that must appear in some diagnostic
		wantOK  bool
		wantErr bool // expect Load itself to error (malformed)
	}{
		{
			name: "happy path",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
platform = ["kmcd/foo", "kmcd/bar"]
[connectors.github]
token = "x"
`,
			wantOK: true,
		},
		{
			name: "window end before start",
			toml: `window = "2025-06-30..2025-01-01"
[teams]
platform = ["kmcd/foo"]
`,
			want: []string{"end date precedes start date"},
		},
		{
			name:    "window malformed",
			toml:    "window = \"not-a-window\"\n[teams]\nt=[\"a/b\"]\n",
			wantErr: true,
		},
		{
			name: "missing window",
			toml: `[teams]
platform = ["kmcd/foo"]
`,
			want: []string{`missing required key "window"`},
		},
		{
			name: "no teams section",
			toml: `window = "2025-01-01..2025-06-30"
`,
			want: []string{`missing required section "[teams]"`},
		},
		{
			name: "empty team",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
platform = []
`,
			want: []string{"team has no repos", "at least one team must contain at least one repo"},
		},
		{
			name: "team name with whitespace",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
"bad name" = ["kmcd/foo"]
`,
			want: []string{"must not contain whitespace"},
		},
		{
			name: "invalid repo slug",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
platform = ["not-a-slug"]
`,
			want: []string{"is not a valid owner/repo slug"},
		},
		{
			// #59: <org>/.github is a real, listable GitHub repo.
			// Validator must accept leading-dot repo names.
			name: "org-config repo with leading-dot name is valid",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
platform = ["goreleaser/.github"]
`,
			want: nil,
		},
		{
			name: "repo in two teams",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
payments = ["kmcd/foo"]
platform = ["kmcd/foo"]
`,
			want: []string{`repo "kmcd/foo" already appears in team "payments"`},
		},
		{
			// empty token = all-empty = pre-staged, not an error
			name: "github pre-staged",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.github]
`,
			wantOK: true,
		},
		{
			name: "github_actions without github",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.github_actions]
`,
			want: []string{"requires [connectors.github] to be configured"},
		},
		{
			name: "github_actions inherits token",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.github]
token = "shared"
[connectors.github_actions]
`,
			wantOK: true,
		},
		{
			// all required fields empty → pre-staged, no error
			name: "circleci pre-staged",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.circleci]
`,
			wantOK: true,
		},
		{
			// token set but no projects → partially configured → error
			name: "circleci token set but no projects",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.circleci]
token = "x"
`,
			want: []string{
				`connectors.circleci.projects: required when [connectors.circleci] is present`,
			},
		},
		{
			name: "circleci project map value not in teams",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.circleci]
token = "x"
[connectors.circleci.projects]
"gh/org/proj" = "owner/missing"
`,
			want: []string{`value "owner/missing" for key "gh/org/proj" does not match any repo in [teams]`},
		},
		{
			name: "circleci project map value is invalid slug",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.circleci]
token = "x"
[connectors.circleci.projects]
"gh/org/proj" = "not-a-slug"
`,
			want: []string{`value "not-a-slug" for key "gh/org/proj" is not a valid owner/repo slug`},
		},
		{
			// when teams is absent teamRepos is empty; project map values with valid slugs
			// should not get a spurious "does not match any repo in [teams]" error
			name: "project map no teams-cross-check when teams missing",
			toml: `window = "2025-01-01..2025-06-30"
[connectors.bugsnag]
token = "x"
[connectors.bugsnag.projects]
"proj-id" = "owner/repo"
`,
			// only the missing teams section error, not a spurious project-map cross-check
			want: []string{`missing required section "[teams]"`},
		},
		{
			name: "sentry pre-staged",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.sentry]
`,
			wantOK: true,
		},
		{
			name: "sentry missing organization and projects",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.sentry]
token = "x"
`,
			want: []string{
				`connectors.sentry.organization: required when [connectors.sentry] is present`,
				`connectors.sentry.projects: required when [connectors.sentry] is present`,
			},
		},
		{
			name: "sentry project map value is invalid slug",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.sentry]
token = "x"
organization = "myorg"
[connectors.sentry.projects]
"my-sentry-proj" = "not-a-slug"
`,
			want: []string{`value "not-a-slug" for key "my-sentry-proj" is not a valid owner/repo slug`},
		},
		{
			name: "sentry project map value not in teams",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.sentry]
token = "x"
organization = "myorg"
[connectors.sentry.projects]
"my-sentry-proj" = "owner/other-repo"
`,
			want: []string{`value "owner/other-repo" for key "my-sentry-proj" does not match any repo in [teams]`},
		},
		{
			name: "sentry project map value matches teams",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.sentry]
token = "x"
organization = "myorg"
[connectors.sentry.projects]
"my-sentry-proj" = "a/b"
`,
			wantOK: true,
		},
		{
			name: "bugsnag pre-staged",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.bugsnag]
`,
			wantOK: true,
		},
		{
			name: "bugsnag missing projects",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.bugsnag]
token = "x"
`,
			want: []string{`connectors.bugsnag.projects: required when [connectors.bugsnag] is present`},
		},
		{
			name: "bugsnag project map value not in teams",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.bugsnag]
token = "x"
[connectors.bugsnag.projects]
"proj-id" = "owner/missing"
`,
			want: []string{`value "owner/missing" for key "proj-id" does not match any repo in [teams]`},
		},
		{
			name: "honeycomb pre-staged",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.honeycomb]
`,
			wantOK: true,
		},
		{
			name: "honeycomb missing dataset",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.honeycomb]
token = "x"
`,
			want: []string{`connectors.honeycomb.dataset: required when [connectors.honeycomb] is present`},
		},
		{
			name: "pr_window valid within global window",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_window = "2024-01-01..2026-06-15"
`,
			wantOK: true,
		},
		{
			name: "pr_window end exceeds global window end",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_window = "2024-01-01..2027-01-01"
`,
			want: []string{"end 2027-01-01 exceeds global window.end 2026-06-15"},
		},
		{
			// pr_window.start before global window.start is silently clamped
			// at runtime (logged as a warning by the connector, not a validation
			// error — blocking the run would prevent the operator from using the
			// config even though the connector handles it gracefully).
			name: "pr_window start before global window start is not a validation error",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_window = "2019-01-01..2026-06-15"
`,
			wantOK: true,
		},
		{
			name: "pr_window malformed",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_window = "not-a-window"
`,
			wantErr: true,
		},
		{
			name: "pr_window inverted (end before start)",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_window = "2026-01-01..2024-01-01"
`,
			want: []string{"end date precedes start date"},
		},
		{
			name: "sparse sampling fully configured",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "2023-06-01"
pr_bracket_window = "12m"
pr_history_sample = "monthly:20"
`,
			wantOK: true,
		},
		{
			name: "pr_inflection without pr_bracket_window",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "2023-06-01"
`,
			want: []string{"pr_inflection requires pr_bracket_window"},
		},
		{
			name: "pr_bracket_window without pr_inflection",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_bracket_window = "12m"
`,
			want: []string{"pr_bracket_window requires pr_inflection"},
		},
		{
			name: "pr_history_sample without pr_inflection",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_history_sample = "monthly:20"
`,
			want: []string{"pr_history_sample requires pr_inflection"},
		},
		{
			name: "pr_window and pr_inflection mutually exclusive",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_window = "2024-01-01..2026-06-15"
pr_inflection = "2023-06-01"
pr_bracket_window = "12m"
`,
			want: []string{"pr_inflection and pr_window are mutually exclusive"},
		},
		{
			name: "pr_inflection outside global window",
			toml: `window = "2024-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "2020-01-01"
pr_bracket_window = "6m"
`,
			want: []string{"inflection date 2020-01-01 is outside global window"},
		},
		{
			name: "pr_bracket_window covers entire window",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "2023-06-01"
pr_bracket_window = "36m"
`,
			want: []string{"bracket start 2020-06-01 reaches or precedes global window start 2021-01-01"},
		},
		{
			name: "pr_inflection malformed date",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "not-a-date"
pr_bracket_window = "12m"
`,
			wantErr: true,
		},
		{
			name: "pr_bracket_window malformed",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "2023-06-01"
pr_bracket_window = "notavalid"
`,
			wantErr: true,
		},
		{
			name: "pr_history_sample with random suffix",
			toml: `window = "2021-01-01..2026-06-15"
[teams]
t = ["a/b"]
[connectors.github]
token = "x"
pr_inflection = "2023-06-01"
pr_bracket_window = "12m"
pr_history_sample = "monthly:20:random"
`,
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTempTOML(t, tc.toml)
			cfg, meta, err := Load(p)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected Load error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			diags := Validate(cfg, meta, p)
			if tc.wantOK {
				if len(diags) != 0 {
					t.Fatalf("expected no diagnostics, got: %v", diags)
				}
				return
			}
			joined := ""
			for _, d := range diags {
				joined += d.Error() + "\n"
			}
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("missing expected diagnostic substring %q in:\n%s", want, joined)
				}
			}
			// Diagnostics must carry positive line numbers.
			for _, d := range diags {
				if d.Line < 1 {
					t.Errorf("diagnostic has bad line: %+v", d)
				}
				if d.File != p {
					t.Errorf("diagnostic file mismatch: %s", d.File)
				}
			}
		})
	}
}

func TestDiagnosticErrorFormat(t *testing.T) {
	d := Diagnostic{File: "xray.toml", Line: 7, Path: "window", Msg: "end date precedes start date"}
	if got := d.Error(); got != "xray.toml:7: window: end date precedes start date" {
		t.Errorf("unexpected: %s", got)
	}
}
