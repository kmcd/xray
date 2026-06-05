package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/run"
)

type runOpts struct {
	out        string
	workers    int
	keepClones bool
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
			ctx := withLogger(cmd.Context(), logger)

			// TODO(M3+): build the connector set from cfg and pass them in.
			// Each connector is constructed from its corresponding cfg.Connectors.*
			// block. For M1 the slice is empty so run produces a manifest-only
			// artifact.
			var connectors []connector.Connector

			artifact, err := run.Run(ctx, cfg, run.Options{
				Out:        outPath,
				Workers:    opts.workers,
				KeepClones: opts.keepClones,
				Connectors: connectors,
				Logger:     logger,
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
	return cmd
}
