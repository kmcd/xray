package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/progress"
	"github.com/kmcd/xray/internal/ratelimit"
	"github.com/kmcd/xray/internal/run"
)

type runOpts struct {
	out           string
	workers       int
	keepClones    bool
	noRunLog      bool
	noCache       bool
	extractShards int
}

func newRunCmd() *cobra.Command {
	var opts runOpts
	cmd := &cobra.Command{
		Use:   "run [config]",
		Short: "Run a full extraction and produce a .tar.gz artifact",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveConfigPath(cmd, args)
			if err != nil {
				return err
			}
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

			connectors, err := buildConnectors(cfg, logger, opts.noCache, resolveExtractShards(opts.extractShards, opts.workers))
			if err != nil {
				return fmt.Errorf("connector setup: %w", err)
			}

			userSink, stopSink := buildProgressSink(ctx, cmd, mode, logger, cfg, connectors, opts.workers)
			defer stopSink()
			rlCounter := progress.NewRateLimitCounter()
			sink := progress.NewTeeSink(userSink, rlCounter)
			ctx = progress.WithSink(ctx, sink)

			result, err := run.Run(ctx, cfg, run.Options{
				Out:         outPath,
				Workers:     opts.workers,
				KeepClones:  opts.keepClones,
				Connectors:  connectors,
				Logger:      logger,
				ToolVersion: version,
				Progress:    sink,
				OnTempDir: func(p string) {
					tmpDirRef.Store(&p)
				},
			})
			// Drain the progress sink before writing the summary line so
			// the TTY grid finalises above the summary instead of overlapping it.
			stopSink()
			// Clear the temp-dir snapshot so a follow-up run in the same
			// process (used by tests, not production) starts fresh and
			// the signal handler can't surface a stale path.
			tmpDirRef.Store(nil)

			logPath := ""
			if logFile != nil {
				logPath = strings.TrimSuffix(outPath, ".tar.gz") + ".log"
			}

			waits, waitSecs := rlCounter.Snapshot()
			result.RateLimitWaits = waits
			result.RateLimitWaitSeconds = waitSecs

			switch {
			case errors.Is(err, context.Canceled):
				fmt.Fprint(errOut, run.InterruptSummary(run.InterruptSummaryInput{
					Phase:    result.InterruptedPhase,
					Inflight: result.InflightJobs,
					TempDir:  result.TempDir,
					Cleaned:  !opts.keepClones,
					ExitCode: 130,
				}))
				return silentCode(err, 130)
			case errors.Is(err, run.ErrPartial):
				if mode != ModeQuiet && mode != ModeJSON {
					fmt.Fprintf(errOut, "run: %v\n", err)
				}
				emitRunSummary(cmd.OutOrStdout(), mode, result, logPath, false)
				return silentCode(err, 2)
			case err != nil:
				fmt.Fprintf(errOut, "run: %v\n", err)
				return silentCode(err, 3)
			}
			emitRunSummary(cmd.OutOrStdout(), mode, result, logPath, true)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.out, "out", "", "output artifact path (default ./xray-export-<UTC-timestamp>.tar.gz)")
	cmd.Flags().IntVar(&opts.workers, "workers", 4, "parallel clone/extract worker count")
	cmd.Flags().BoolVar(&opts.keepClones, "keep-clones", false, "skip cleanup of temp clones (debugging)")
	cmd.Flags().BoolVar(&opts.noRunLog, "no-run-log", false, "disable run log file (mirrors stderr output alongside the artifact by default)")
	cmd.Flags().BoolVar(&opts.noCache, "no-cache", false, "disable on-disk caches (currently: Honeycomb markers)")
	cmd.Flags().IntVar(&opts.extractShards, "extract-shards", 0, "concurrent git subprocesses per repo for complexity and walk phases (0 = auto)")
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
		tty.SetBudgetSource(buildBudgetSource(connectors))
		tty.Start(ctx)
		return tty, tty.Stop
	}
}

// budgetSnapshotter is the optional interface a connector may implement to
// expose its underlying ratelimit.Transport snapshot. TTYSink polls this
// on each tick to render a live per-connector budget readout.
type budgetSnapshotter interface {
	BudgetSnapshot() map[string]ratelimit.BudgetState
}

// buildBudgetSource builds a progress.BudgetSource that aggregates snapshots
// from all connectors that implement budgetSnapshotter.
func buildBudgetSource(connectors []connector.Connector) progress.BudgetSource {
	var sources []budgetSnapshotter
	for _, c := range connectors {
		if bs, ok := c.(budgetSnapshotter); ok {
			sources = append(sources, bs)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return func() map[string]progress.BudgetEntry {
		out := make(map[string]progress.BudgetEntry)
		for _, s := range sources {
			for k, v := range s.BudgetSnapshot() {
				out[k] = progress.BudgetEntry{
					Remaining:    v.Remaining,
					HasRemaining: v.HasRemaining,
					Limit:        v.Limit,
					ResetAt:      v.ResetAt,
				}
			}
		}
		return out
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

// resolveExtractShards applies the auto-rule when shards==0 and returns the
// number of concurrent git subprocesses to use per repo for the complexity
// history and working-tree phases.
//
// Auto-rule (shards==0):
//   - workers==1  → min(runtime.NumCPU(), 4)   (single-monolith case)
//   - workers>1   → max(1, runtime.NumCPU()/workers)
//   - hard cap: 4 (subprocess pipes, diminishing returns past 4×)
func resolveExtractShards(shards, workers int) int {
	if shards > 0 {
		return shards
	}
	n := runtime.NumCPU()
	var s int
	if workers <= 1 {
		s = n
	} else {
		s = n / workers
	}
	if s < 1 {
		s = 1
	}
	if s > 4 {
		s = 4
	}
	return s
}

// emitRunSummary writes the final per-mode output for a run.
// ModeAuto / ModeLog: the full post-run summary block on stdout (issue #84).
// ModeQuiet: bare artifact path on stdout, nothing else.
// ModeJSON: one run_summary JSON object on stdout.
func emitRunSummary(w io.Writer, mode Mode, result run.Result, logPath string, ok bool) {
	if result.ArtifactPath == "" {
		return
	}
	in := run.SummaryInput{
		Manifest:             result.Manifest,
		ArtifactPath:         result.ArtifactPath,
		SHA256:               result.SHA256,
		Size:                 result.Size,
		Duration:             result.Duration,
		LogPath:              logPath,
		PartialFails:         run.ExtractPartialFailures(result.Manifest.Provenance),
		RateLimitWaits:       result.RateLimitWaits,
		RateLimitWaitSeconds: result.RateLimitWaitSeconds,
	}
	switch mode {
	case ModeQuiet:
		fmt.Fprintln(w, result.ArtifactPath)
	case ModeJSON:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(run.BuildRunSummary(in, ok))
	default:
		fmt.Fprint(w, run.Summarize(in))
	}
}
