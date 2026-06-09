package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/run"
)

type runOpts struct {
	out        string
	workers    int
	keepClones bool
	noRunLog   bool
}

func newRunCmd() *cobra.Command {
	var opts runOpts
	cmd := &cobra.Command{
		Use:   "run <config>",
		Short: "Run a full extraction and produce a .tar.gz artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
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

			outPath := opts.out
			if outPath == "" {
				outPath = fmt.Sprintf("./xray-export-%s.tar.gz",
					time.Now().UTC().Format("20060102T150405Z"))
			}

			logger := run.NewLogger(flagVerbose, flagQuiet)
			if !opts.noRunLog {
				logPath := strings.TrimSuffix(outPath, ".tar.gz") + ".log"
				// #nosec G304 -- logPath derived from user-supplied --out; O_APPEND preserves prior runs.
				lf, ferr := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
				if ferr != nil {
					fmt.Fprintf(errOut, "warn: could not create run log %s: %v\n", logPath, ferr)
				} else {
					defer lf.Close()
					logger = run.NewLogger(flagVerbose, flagQuiet, lf)
				}
			}

			ctx := withLogger(cmd.Context(), logger)

			connectors, err := buildConnectors(cfg, logger)
			if err != nil {
				return fmt.Errorf("connector setup: %w", err)
			}

			artifact, err := run.Run(ctx, cfg, run.Options{
				Out:         outPath,
				Workers:     opts.workers,
				KeepClones:  opts.keepClones,
				Connectors:  connectors,
				Logger:      logger,
				ToolVersion: version,
			})
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", artifact)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.out, "out", "", "output artifact path (default ./xray-export-<UTC-timestamp>.tar.gz)")
	cmd.Flags().IntVar(&opts.workers, "workers", 4, "parallel clone/extract worker count")
	cmd.Flags().BoolVar(&opts.keepClones, "keep-clones", false, "skip cleanup of temp clones (debugging)")
	cmd.Flags().BoolVar(&opts.noRunLog, "no-run-log", false, "disable run log file (mirrors stderr output alongside the artifact by default)")
	return cmd
}
