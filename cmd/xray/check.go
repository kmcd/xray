// Package main implements the xray CLI subcommands.
package main

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/gitcli"
	"github.com/kmcd/xray/internal/run"
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

			anyFail := false
			if _, err := exec.LookPath("git"); err != nil {
				anyFail = true
				fmt.Fprintln(out, "FAIL git              not found on PATH")
			} else {
				fmt.Fprintln(out, "ok  git              on PATH")
			}

			logger := run.NewLogger(flagVerbose, flagQuiet)
			conns, err := buildConnectors(cfg, logger)
			if err != nil {
				fmt.Fprintf(errOut, "connector setup: %v\n", err)
				return silentCode(err, 2)
			}
			ctx := cmd.Context()
			for _, c := range conns {
				if err := c.Ping(ctx); err != nil {
					anyFail = true
					fmt.Fprintf(out, "FAIL %-16s %v\n", c.Name(), err)
				} else {
					fmt.Fprintf(out, "ok  %-16s authenticated (read-only)\n", c.Name())
				}
			}

			gitClient := &gitcli.Client{Log: logger}
			repos := cfg.AllRepos()
			sort.Strings(repos)
			for _, r := range repos {
				if err := gitClient.LsRemote(ctx, r); err != nil {
					anyFail = true
					fmt.Fprintf(out, "FAIL %-16s %v\n", r, err)
				} else {
					fmt.Fprintf(out, "ok  %-16s clone access ok\n", r)
				}
			}

			if anyFail {
				return silentCode(errors.New("preflight failed"), 2)
			}
			return nil
		},
	}
}

