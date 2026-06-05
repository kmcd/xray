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
func NewLogger(verbose, quiet bool) *slog.Logger {
	var w io.Writer = os.Stderr
	level := slog.LevelInfo
	switch {
	case quiet:
		level = slog.LevelError
	case verbose:
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}
