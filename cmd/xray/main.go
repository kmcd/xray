package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"
)

// Build-time variables, injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Persistent flags shared across subcommands.
var (
	flagVerbose bool
	flagQuiet   bool
	flagOutput  string
)

// tmpDirRef carries the per-run temp directory path from internal/run.Run
// up to the signal handler so the double-Ctrl-C force-exit log line can
// name the leaked path. Updated via the run.Options.OnTempDir callback.
// Cleared once Run returns so a subsequent run in the same process (used
// by tests) doesn't see a stale path.
var tmpDirRef atomic.Pointer[string]

// loggerKey is the context key used to thread the slog.Logger through to
// subcommand RunE bodies.
type loggerKey struct{}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "xray",
		Short: "Extract a portable engineering-metrics artifact",
		Long: `xray is a read-only extractor that produces a portable, inspectable
metrics artifact from a client's engineering systems. The artifact is a
single .tar.gz containing a SQLite database and a JSON manifest; it
contains no source code and no secrets.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose logging (per-API-call timing)")
	root.PersistentFlags().BoolVar(&flagQuiet, "quiet", false, "shorthand for --output quiet")
	root.PersistentFlags().StringVar(&flagOutput, "output", "", "output mode: auto|quiet|json|log (default auto)")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if _, err := ResolveMode(flagOutput, flagQuiet); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			return silentCode(err, 1)
		}
		return nil
	}

	root.AddCommand(
		newVersionCmd(),
		newValidateCmd(),
		newInitCmd(),
		newCheckCmd(),
		newRunCmd(),
	)
	return root
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go watchSignals(sigs, cancel, tmpDirSnapshot, os.Stderr, os.Exit)

	root := newRootCmd()
	if err := root.ExecuteContext(ctx); err != nil {
		// Subcommands that already printed their own diagnostics return
		// silentErr so we don't double-report. Anything else surfaces here.
		if !isSilent(err) {
			fmt.Fprintln(os.Stderr, "xray:", err)
		}
		os.Exit(exitCodeFor(err))
	}
}

// watchSignals drains sigs and implements the double-Ctrl-C state
// machine: first signal calls cancel for a cooperative shutdown; any
// subsequent signal force-exits via exit(130), bypassing defers so any
// half-cleaned temp dir leaks visibly (we name it via tmpDir()).
//
// Pulled out of main() so the state machine is unit-testable with an
// injected channel + exit func.
func watchSignals(sigs <-chan os.Signal, cancel context.CancelFunc, tmpDir func() string, stderr io.Writer, exit func(int)) {
	first := true
	for range sigs {
		if first {
			first = false
			fmt.Fprintln(stderr, "xray: interrupt received, finishing in-flight work; press Ctrl-C again to force exit")
			cancel()
			continue
		}
		path := tmpDir()
		if path == "" {
			fmt.Fprintln(stderr, "xray: force exit; temp dir not cleaned")
		} else {
			fmt.Fprintf(stderr, "xray: force exit; temp dir %s not cleaned\n", path)
		}
		exit(130)
		return
	}
}

// tmpDirSnapshot reads the current temp-dir path written by the
// OnTempDir callback. Returns "" before run.Run reaches that point.
func tmpDirSnapshot() string {
	p := tmpDirRef.Load()
	if p == nil {
		return ""
	}
	return *p
}

// withLogger attaches a slog.Logger to a context for subcommand use.
func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}
