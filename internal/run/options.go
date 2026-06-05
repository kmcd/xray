package run

import (
	"log/slog"

	"github.com/kmcd/xray/internal/connector"
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
}
