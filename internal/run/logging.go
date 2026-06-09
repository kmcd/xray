package run

import (
	"io"
	"log/slog"
	"os"
)

// NewLogger configures slog for a run. verbose lowers the level to Debug
// (per-API-call timing), quiet raises it to Error. The default is Info.
// Logs always go to stderr; tokens are never emitted by any caller in this
// package and there is no logging code path that accepts tokens.
// An optional extra writer receives the same output (used for the run log file).
func NewLogger(verbose, quiet bool, extra ...io.Writer) *slog.Logger {
	var w io.Writer = os.Stderr
	if len(extra) > 0 {
		w = io.MultiWriter(append([]io.Writer{os.Stderr}, extra...)...)
	}
	return newLoggerToWriter(w, verbose, quiet)
}

// NewLoggerNoStderr configures slog without writing to stderr. Used by
// cmd/xray when the TTY status grid owns stdout and stderr lines would
// visibly leak into the rendered grid; the run log file (or any other
// passed writers) still captures everything.
func NewLoggerNoStderr(verbose, quiet bool, writers ...io.Writer) *slog.Logger {
	var w io.Writer
	switch len(writers) {
	case 0:
		w = io.Discard
	case 1:
		w = writers[0]
	default:
		w = io.MultiWriter(writers...)
	}
	return newLoggerToWriter(w, verbose, quiet)
}

func newLoggerToWriter(w io.Writer, verbose, quiet bool) *slog.Logger {
	level := slog.LevelInfo
	switch {
	case quiet:
		level = slog.LevelError
	case verbose:
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
