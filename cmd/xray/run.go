package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/progress"
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

			// TTY-grid mode owns stdout for the grid and would clash with
			// any stderr log lines on the same terminal. When active, route
			// the logger only to the run log file (or discard, if
			// --no-run-log).
			ttyGrid := mode == ModeAuto && stdoutIsTTY(cmd)

			var logFile *os.File
			if !opts.noRunLog {
				logPath := strings.TrimSuffix(outPath, ".tar.gz") + ".log"
				// #nosec G304 -- logPath derived from user-supplied --out; O_APPEND preserves prior runs.
				lf, ferr := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
				if ferr != nil {
					fmt.Fprintf(errOut, "warn: could not create run log %s: %v\n", logPath, ferr)
				} else {
					defer lf.Close()
					logFile = lf
				}
			}

			var logger *slog.Logger
			switch {
			case ttyGrid && logFile != nil:
				logger = run.NewLoggerNoStderr(flagVerbose, loggerQuiet, logFile)
			case ttyGrid:
				// --no-run-log + auto + TTY: nowhere to land logs without
				// clobbering the grid. Drop them; the grid is the UI.
				logger = run.NewLoggerNoStderr(flagVerbose, loggerQuiet)
			case logFile != nil:
				logger = run.NewLogger(flagVerbose, loggerQuiet, logFile)
			default:
				logger = run.NewLogger(flagVerbose, loggerQuiet)
			}

			ctx := withLogger(cmd.Context(), logger)

			connectors, err := buildConnectors(cfg, logger)
			if err != nil {
				return fmt.Errorf("connector setup: %w", err)
			}

			sink, stopSink := buildProgressSink(ctx, cmd, mode, logger, cfg, connectors, opts.workers)
			defer stopSink()

			artifact, err := run.Run(ctx, cfg, run.Options{
				Out:         outPath,
				Workers:     opts.workers,
				KeepClones:  opts.keepClones,
				Connectors:  connectors,
				Logger:      logger,
				ToolVersion: version,
				Progress:    sink,
			})
			// Drain the progress sink before writing the summary line so
			// the TTY grid finalises above "wrote …" instead of overlapping it.
			stopSink()

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

// buildProgressSink wires the right progress.Sink for the resolved
// output mode. ModeAuto on a TTY gets the live status grid; everywhere
// else (non-TTY auto, log, json, quiet) gets the appropriate fallback.
// Returns the sink and a stop function — call stop before writing the
// summary line so the grid finalises cleanly. stop is safe to call
// multiple times.
func buildProgressSink(
	ctx context.Context,
	cmd *cobra.Command,
	mode Mode,
	logger *slog.Logger,
	cfg *config.Config,
	connectors []connector.Connector,
	workers int,
) (progress.Sink, func()) {
	stdout := cmd.OutOrStdout()
	switch mode {
	case ModeQuiet:
		return progress.NopSink{}, func() {}
	case ModeJSON:
		return progress.NewJSONSink(stdout), func() {}
	case ModeLog:
		return progress.NewLogSink(logger, flagVerbose), func() {}
	default: // ModeAuto
		if !stdoutIsTTY(cmd) {
			return progress.NewLogSink(logger, flagVerbose), func() {}
		}
		f := stdout.(*os.File)
		tty := progress.NewTTYSink(f)
		tty.Plan(cfg.AllRepos(), connectorNames(connectors), workers)
		tty.Start(ctx)
		return tty, tty.Stop
	}
}

// stdoutIsTTY reports whether the command's stdout is a real terminal.
// Used to decide between the TTY grid and the line-log fallback in
// ModeAuto. Returns false whenever stdout is anything other than an
// *os.File (the typical case in tests).
func stdoutIsTTY(cmd *cobra.Command) bool {
	f, ok := cmd.OutOrStdout().(*os.File)
	if !ok {
		return false
	}
	return IsTTY(f)
}

func connectorNames(cs []connector.Connector) []string {
	out := make([]string, 0, len(cs)+1)
	out = append(out, "clone")
	for _, c := range cs {
		out = append(out, c.Name())
	}
	return out
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
