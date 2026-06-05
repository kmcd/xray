package main

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
)

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <config>",
		Short: "Live preflight against configured connectors and repos",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			cfg, meta, err := config.Load(path)
			if err != nil {
				fmt.Fprintf(errOut, "%s: %v\n", path, err)
				return silentCode(errors.New("config load failed"), 2)
			}
			diags := config.Validate(cfg, meta, path)
			if len(diags) > 0 {
				for _, d := range diags {
					fmt.Fprintln(errOut, d.Error())
				}
				return silentCode(errors.New("config invalid"), 2)
			}
			fmt.Fprintln(out, "ok  config valid")

			gitOK := true
			if _, err := exec.LookPath("git"); err != nil {
				gitOK = false
				fmt.Fprintln(out, "FAIL git              not found on PATH")
			} else {
				fmt.Fprintln(out, "ok  git              on PATH")
			}

			// TODO(M3+): wire each configured connector into the registry and
			// run its Ping(); for now we just list what is configured.
			for _, name := range configuredConnectors(cfg) {
				fmt.Fprintf(out, "ok  %-16s pending: connectors not yet wired (waves 2 of impl)\n", name)
			}

			// TODO(M3+): run `git ls-remote` per repo to verify clone access.
			repos := cfg.AllRepos()
			sort.Strings(repos)
			for _, r := range repos {
				fmt.Fprintf(out, "ok  %-16s pending: clone-access probe not yet wired\n", r)
			}

			if !gitOK {
				return silentCode(errors.New("preflight failed"), 2)
			}
			return nil
		},
	}
}

func configuredConnectors(cfg *config.Config) []string {
	var names []string
	c := cfg.Connectors
	if c.GitHub != nil {
		names = append(names, "github")
	}
	if c.GitHubActions != nil {
		names = append(names, "github_actions")
	}
	if c.CircleCI != nil {
		names = append(names, "circleci")
	}
	if c.Sentry != nil {
		names = append(names, "sentry")
	}
	if c.Bugsnag != nil {
		names = append(names, "bugsnag")
	}
	if c.Honeycomb != nil {
		names = append(names, "honeycomb")
	}
	return names
}
