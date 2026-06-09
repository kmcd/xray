package progress

import (
	"log/slog"
	"sync"
)

// LogSink emits one slog line per transition event. PhaseProgress events
// are dropped unless verbose is set; this matches the issue's
// acceptance criterion "Non-TTY: one line per state transition…
// Skip PhaseProgress ticks unless --verbose."
type LogSink struct {
	log     *slog.Logger
	verbose bool
	mu      sync.Mutex
}

// NewLogSink wires a sink to an existing slog logger. The logger
// continues to receive xray's existing extraction lines independently;
// LogSink's output is additive — one line per phase boundary.
func NewLogSink(log *slog.Logger, verbose bool) *LogSink {
	if log == nil {
		log = slog.Default()
	}
	return &LogSink{log: log, verbose: verbose}
}

// Emit writes one slog line for transitions; PhaseError uses Warn,
// everything else uses Info. PhaseProgress is dropped unless verbose.
func (s *LogSink) Emit(e Event) {
	if !e.IsTransition() && !s.verbose {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	attrs := []any{
		slog.String("kind", string(e.Kind)),
	}
	if e.Phase != "" {
		attrs = append(attrs, slog.String("phase", e.Phase))
	}
	if e.Repo != "" {
		attrs = append(attrs, slog.String("repo", e.Repo))
	}
	if e.Connector != "" {
		attrs = append(attrs, slog.String("connector", e.Connector))
	}
	if e.Total > 0 {
		attrs = append(attrs, slog.Int64("done", e.Done), slog.Int64("total", e.Total))
	} else if e.Done > 0 {
		attrs = append(attrs, slog.Int64("rows", e.Done))
	}
	if e.Message != "" {
		attrs = append(attrs, slog.String("msg", e.Message))
	}

	switch e.Kind {
	case PhaseError:
		s.log.Warn("progress", attrs...)
	case RateLimit:
		s.log.Warn("progress", attrs...)
	default:
		s.log.Info("progress", attrs...)
	}
}
