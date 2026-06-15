package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

type initOpts struct {
	org             string
	out             string
	token           string
	force           bool
	probe           bool
	includeForks    bool
	includeArchived bool
}

// initResult is the JSON shape emitted by `xray init --output json`.
type initResult struct {
	Kind        string `json:"kind"`
	OK          bool   `json:"ok"`
	ConfigPath  string `json:"config_path"`
	Overwritten bool   `json:"overwritten"`
}

func newInitCmd() *cobra.Command {
	var opts initOpts
	cmd := &cobra.Command{
		Use:   "init --from-org <github-org>",
		Short: "Generate a starter TOML config from a GitHub org",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.org == "" {
				return errors.New("--from-org is required")
			}
			if opts.probe {
				return runProbe(cmd, opts)
			}
			mode, err := ResolveMode(flagOutput, flagQuiet)
			if err != nil {
				return silentCode(err, 1)
			}
			tok := opts.token
			if tok == "" {
				tok = os.Getenv("XRAY_GH_TOKEN")
			}
			if tok == "" {
				tok = os.Getenv("GITHUB_TOKEN")
			}
			if tok == "" {
				return errors.New("no GitHub token provided (set --token, $XRAY_GH_TOKEN, or $GITHUB_TOKEN)")
			}
			overwritten := false
			if _, statErr := os.Stat(opts.out); statErr == nil {
				if !opts.force {
					return fmt.Errorf("%s already exists (pass --force to overwrite)", opts.out)
				}
				overwritten = true
			}

			ctx := cmd.Context()
			repos, err := listOrgRepos(ctx, tok, opts.org, opts.includeForks, opts.includeArchived)
			if err != nil {
				return fmt.Errorf("listing repos for %s: %w", opts.org, err)
			}
			if len(repos) == 0 {
				return fmt.Errorf("no repos found under org %q", opts.org)
			}
			sort.Strings(repos)

			body, err := renderScaffold(opts.org, repos)
			if err != nil {
				return err
			}
			// #nosec G703 -- opts.out is the user-supplied --out path; writing
			// to it is the intended behaviour of `xray init`.
			if err := os.WriteFile(opts.out, []byte(body), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", opts.out, err)
			}
			switch mode {
			case ModeQuiet:
				// success: nothing on stdout.
			case ModeJSON:
				_ = emitJSONLine(cmd.OutOrStdout(), initResult{
					Kind: "init_summary", OK: true, ConfigPath: opts.out, Overwritten: overwritten,
				})
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d repos under team \"unassigned\")\n", opts.out, len(repos))
				fmt.Fprintln(cmd.OutOrStdout(), "next: edit the file to set tokens, split repos into teams, and pick a window")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.org, "from-org", "", "GitHub organisation to discover repos from (required)")
	cmd.Flags().StringVar(&opts.out, "out", "xray.toml", "output path for the generated config")
	cmd.Flags().StringVar(&opts.token, "token", "", "GitHub token (else $XRAY_GH_TOKEN or $GITHUB_TOKEN)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "overwrite the output file if it already exists")
	cmd.Flags().BoolVar(&opts.probe, "probe", false, "discover connector data live and scaffold config from real observations")
	cmd.Flags().BoolVar(&opts.includeForks, "include-forks", false, "include forked repositories (excluded by default)")
	cmd.Flags().BoolVar(&opts.includeArchived, "include-archived", false, "include archived repositories (excluded by default)")
	return cmd
}

// newGitHubClient builds an authenticated go-github client. It is a package
// variable so tests can swap in a client pointed at httptest.NewServer.
var newGitHubClient = func(ctx context.Context, token string) *github.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

// listOrgRepos paginates the GitHub REST repos-by-org listing and returns
// owner/repo slugs in stable order. Forks and archived repos are excluded by
// default; pass includeForks or includeArchived to override.
func listOrgRepos(ctx context.Context, token, org string, includeForks, includeArchived bool) ([]string, error) {
	client := newGitHubClient(ctx, token)

	var out []string
	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	if !includeForks {
		opt.Type = "sources" // excludes forked repositories at the API level
	} else {
		opt.Type = "all"
	}
	for {
		page, resp, err := client.Repositories.ListByOrg(ctx, org, opt)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			if r == nil || r.FullName == nil {
				continue
			}
			if !includeForks && r.GetFork() {
				continue // defense-in-depth: also filter client-side
			}
			if !includeArchived && r.GetArchived() {
				continue
			}
			out = append(out, r.GetFullName())
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return out, nil
}

const scaffoldTmpl = `# generated by xray init --from-org {{.Org}}
# review and edit before running:
#   - split [teams] into real team groupings
#   - paste a token into each connector block you want to enable
#   - delete connector blocks you do not need

# Extraction window: starts 2021-01-01 to capture the pre-Copilot baseline (~18 months before
# Copilot GA in June 2022). On a large repo, validate connectors with a shorter window first.
window = "{{.WindowDefault}}"

# Set true to capture the content of harness/AI-tool config files
# (CLAUDE.md, .cursor/rules, etc.) in addition to metadata. Default false.
capture_harness_content = false

[teams]
unassigned = [
{{- range .Repos }}
    "{{.}}",
{{- end }}
]

[connectors.github]
token = ""

[connectors.github_actions]
# Inherits token from [connectors.github] by default; set token here to override.

[connectors.circleci]
token = ""
[connectors.circleci.projects]
# "gh/owner/repo" = "owner/repo"

[connectors.sentry]
token = ""
organization = ""
[connectors.sentry.projects]
# "sentry-project-slug" = "owner/repo"

[connectors.bugsnag]
token = ""
[connectors.bugsnag.projects]
# "bugsnag-project-id" = "owner/repo"

[connectors.honeycomb]
token = ""
dataset = ""
`

func renderScaffold(org string, repos []string) (string, error) {
	t, err := template.New("scaffold").Parse(scaffoldTmpl)
	if err != nil {
		return "", err
	}
	today := time.Now().UTC().Format("2006-01-02")
	var buf strings.Builder
	if err := t.Execute(&buf, struct {
		Org           string
		Repos         []string
		WindowDefault string
	}{Org: org, Repos: repos, WindowDefault: "2021-01-01.." + today}); err != nil {
		return "", err
	}
	return buf.String(), nil
}
