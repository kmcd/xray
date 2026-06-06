package manifest

import (
	"encoding/json"
	"io"
	"time"

	"github.com/kmcd/xray/internal/connector"
)

// Manifest is the exact shape written to manifest.json. Field names and
// nesting must match the spec in docs/spec.md "manifest.json".
type Manifest struct {
	ToolVersion    string                 `json:"tool_version"`
	SchemaVersion  int                    `json:"schema_version"`
	RunID          string                 `json:"run_id"`
	RunStartedAt   time.Time              `json:"run_started_at"`
	RunCompletedAt time.Time              `json:"run_completed_at"`
	Window         WindowJSON             `json:"window"`
	Teams          map[string][]string    `json:"teams"`
	Repos          []RepoMeta             `json:"repos"`
	ConnectorsUsed []string               `json:"connectors_used"`
	Counts         map[string]int         `json:"counts"`
	Provenance     []connector.Provenance `json:"extraction_provenance"`
}

// WindowJSON renders the inclusive UTC window as YYYY-MM-DD start/end.
type WindowJSON struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// RepoMeta is the per-repo metadata entry in manifest.json.
type RepoMeta struct {
	Slug          string `json:"slug"`
	HeadSHA       string `json:"head_sha"`
	DefaultBranch string `json:"default_branch"`
}

// WriteTo emits indented JSON to w with a trailing newline. The return value
// is the number of bytes written (per io.WriterTo).
func (m *Manifest) WriteTo(w io.Writer) (int64, error) {
	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, err
	}
	buf = append(buf, '\n')
	n, err := w.Write(buf)
	return int64(n), err
}
