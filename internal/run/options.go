package run

import (
	"log/slog"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/progress"
)

// Options configures a single run. Zero values pick spec-mandated defaults
// (4 workers, ./xray-export-<UTC>.tar.gz output, info-level stderr logger).
type Options struct {
	Out         string
	Workers     int
	KeepClones  bool
	Connectors  []connector.Connector
	Logger      *slog.Logger
	ToolVersion string
	// Progress is the sink for run-time phase events. Nil resolves to a
	// no-op; the CLI selects a TTY grid / line log / NDJSON / no-op sink
	// based on the --output mode resolved in cmd/xray/output.go.
	Progress progress.Sink
	// OnTempDir, if non-nil, is invoked exactly once with the absolute
	// path of the per-run temp directory immediately after creation. The
	// CLI uses it so the signal handler can name the leaked path in the
	// double-Ctrl-C force-exit log line.
	OnTempDir func(string)
}
