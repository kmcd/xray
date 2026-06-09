package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/run"
)

// nowUTC is overridden in tests for deterministic JSON output.
var nowUTC = func() time.Time { return time.Now().UTC() }

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

			mode, err := ResolveMode(flagOutput, flagQuiet)
			if err != nil {
				// Already reported by PersistentPreRunE; defensive only.
				return silentCode(err, 1)
			}

			cfg, meta, err := config.Load(path)
			if err != nil {
				fmt.Fprintf(errOut, "%s: %v\n", path, err)
				return silentCode(errors.New("config load failed"), 1)
			}
			diags := config.Validate(cfg, meta, path)
			if len(diags) > 0 {
				for _, d := range diags {
					fmt.Fprintln(errOut, d.Error())
				}
				return silentCode(errors.New("config invalid"), 1)
			}

			outPath := opts.out
			if outPath == "" {
				outPath = fmt.Sprintf("./xray-export-%s.tar.gz",
					time.Now().UTC().Format("20060102T150405Z"))
			}

			loggerQuiet := flagQuiet || mode == ModeQuiet || mode == ModeJSON
			logger := run.NewLogger(flagVerbose, loggerQuiet)
			if !opts.noRunLog {
				logPath := strings.TrimSuffix(outPath, ".tar.gz") + ".log"
				// #nosec G304 -- logPath derived from user-supplied --out; O_APPEND preserves prior runs.
				lf, ferr := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
				if ferr != nil {
					fmt.Fprintf(errOut, "warn: could not create run log %s: %v\n", logPath, ferr)
				} else {
					defer lf.Close()
					logger = run.NewLogger(flagVerbose, loggerQuiet, lf)
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
			switch {
			case errors.Is(err, run.ErrPartial):
				if mode != ModeQuiet && mode != ModeJSON {
					fmt.Fprintf(errOut, "run: %v\n", err)
				}
				emitRunSummary(cmd.OutOrStdout(), mode, artifact, 2)
				return silentCode(err, 2)
			case err != nil:
				fmt.Fprintf(errOut, "run: %v\n", err)
				return silentCode(err, 3)
			}
			emitRunSummary(cmd.OutOrStdout(), mode, artifact, 0)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.out, "out", "", "output artifact path (default ./xray-export-<UTC-timestamp>.tar.gz)")
	cmd.Flags().IntVar(&opts.workers, "workers", 4, "parallel clone/extract worker count")
	cmd.Flags().BoolVar(&opts.keepClones, "keep-clones", false, "skip cleanup of temp clones (debugging)")
	cmd.Flags().BoolVar(&opts.noRunLog, "no-run-log", false, "disable run log file (mirrors stderr output alongside the artifact by default)")
	return cmd
}

// emitRunSummary writes the final per-mode output line(s) for a run.
// ModeAuto / ModeLog: the historical "wrote <path>" line on stdout.
// ModeQuiet: bare artifact path on stdout, nothing else.
// ModeJSON: one SummaryEvent NDJSON line on stdout. The full progress
// event stream (phase_start, phase_progress) lands with #81.
func emitRunSummary(w io.Writer, mode Mode, artifact string, exitCode int) {
	if artifact == "" {
		return
	}
	switch mode {
	case ModeQuiet:
		fmt.Fprintln(w, artifact)
	case ModeJSON:
		_ = EmitSummary(w, SummaryEvent{
			TS:       nowUTC().Format(time.RFC3339),
			Artifact: artifact,
			ExitCode: exitCode,
		})
	default:
		fmt.Fprintf(w, "wrote %s\n", artifact)
	}
}
