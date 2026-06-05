package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
)

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
	root.PersistentFlags().BoolVar(&flagQuiet, "quiet", false, "suppress non-error output")

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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

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

// withLogger attaches a slog.Logger to a context for subcommand use.
func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}
