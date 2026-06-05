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
			name: "repo in two teams",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
payments = ["kmcd/foo"]
platform = ["kmcd/foo"]
`,
			want: []string{`repo "kmcd/foo" already appears in team "payments"`},
		},
		{
			name: "github without token",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.github]
`,
			want: []string{`connectors.github: missing required key "token"`},
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
			name: "sentry missing organization and projects",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.sentry]
token = "x"
`,
			want: []string{
				`connectors.sentry: missing required key "organization"`,
				`connectors.sentry: missing required key "projects"`,
			},
		},
		{
			name: "bugsnag missing projects",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.bugsnag]
token = "x"
`,
			want: []string{`connectors.bugsnag: missing required key "projects"`},
		},
		{
			name: "honeycomb missing dataset",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.honeycomb]
token = "x"
`,
			want: []string{`missing required key "dataset"`},
		},
		{
			name: "circleci missing token",
			toml: `window = "2025-01-01..2025-06-30"
[teams]
t = ["a/b"]
[connectors.circleci]
`,
			want: []string{`connectors.circleci: missing required key "token"`},
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
