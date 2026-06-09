package run

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/manifest"
)

// summaryTopRows caps the row-roll-up at the eight largest tables; the rest
// fold into a "(N tables total)" tail. The spec example in issue #84 shows
// seven non-zero tables plus the count line — eight gives us one row of
// slack while keeping the block to a single screen.
const summaryTopRows = 8

// SummaryInput is everything Summarize needs. It is a thin pass-through so
// the caller assembles the inputs once and the renderer stays pure.
type SummaryInput struct {
	Manifest     manifest.Manifest
	ArtifactPath string
	SHA256       string
	Size         int64
	Duration     time.Duration
	LogPath      string
	PartialFails []PartialFailure
}

// PartialFailure identifies one (repo, connector) pair that errored during
// a partial run.
type PartialFailure struct {
	Repo      string
	Connector string
	Reason    string
}

// RunSummary is the JSON-shape of the run-summary event emitted in
// --output json mode. The wire shape is documented in docs/spec.md and
// versioned independently of the artifact SchemaVersion.
type RunSummary struct {
	Kind       string             `json:"kind"`
	OK         bool               `json:"ok"`
	DurationS  int                `json:"duration_s"`
	Artifact   SummaryArtifact    `json:"artifact"`
	Rows       map[string]any     `json:"rows"`
	Provenance SummaryProvenance  `json:"provenance"`
	Partial    []PartialFailure   `json:"partial"`
}

// SummaryArtifact mirrors the human-readable Artifact block.
type SummaryArtifact struct {
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	SchemaVersion int    `json:"schema_version"`
	LogPath       string `json:"log_path,omitempty"`
}

// SummaryProvenance collapses the per-row provenance stream into the
// aggregate counters the customer sees. RateLimitTruncated counts
// connectors whose pagination was cut off by the rate-limit budget; the
// raw wait count + cumulative wait time will be added once #82's events
// flow back into the manifest's provenance.
type SummaryProvenance struct {
	EndpointsAccessible   int `json:"endpoints_accessible"`
	EndpointsTotal        int `json:"endpoints_total"`
	EndpointsInaccessible int `json:"endpoints_inaccessible"`
	PerRowErrors          int `json:"per_row_errors"`
	RateLimitTruncated    int `json:"rate_limit_truncated"`
	PartialPaginations    int `json:"partial_paginations"`
}

// Summarize renders the post-run summary block. Pure: no filesystem, no DB,
// no network. Caller is responsible for assembling the inputs.
func Summarize(in SummaryInput) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Done in %s\n\n", formatDuration(in.Duration))

	fmt.Fprintln(&b, "Artifact")
	fmt.Fprintf(&b, "  path:     %s\n", in.ArtifactPath)
	fmt.Fprintf(&b, "  size:     %s\n", formatSize(in.Size))
	fmt.Fprintf(&b, "  sha256:   %s (verify with: sha256sum)\n", in.SHA256)
	fmt.Fprintf(&b, "  schema:   v%d\n", in.Manifest.SchemaVersion)
	if in.LogPath != "" {
		fmt.Fprintf(&b, "  log:      %s\n", in.LogPath)
	}
	b.WriteString("\n")

	writeRowsBlock(&b, in.Manifest.Counts)
	b.WriteString("\n")

	writeProvenanceBlock(&b, summarizeProvenance(in.Manifest.Provenance))
	b.WriteString("\n")

	if len(in.PartialFails) > 0 {
		fmt.Fprintln(&b, "Partial")
		for _, p := range in.PartialFails {
			fmt.Fprintf(&b, "  %s / %s: %s\n", p.Repo, p.Connector, p.Reason)
		}
		fmt.Fprintln(&b, "  See manifest.json -> extraction_provenance for full details.")
		b.WriteString("\n")
	}

	fmt.Fprintln(&b, "Next")
	artifactName := lastPathSegment(in.ArtifactPath)
	fmt.Fprintf(&b, "  Send %s to your consultant.\n", artifactName)
	fmt.Fprintln(&b, "  Do NOT send your config file — it contains your API tokens.")

	return b.String()
}

// BuildRunSummary assembles the JSON-shape companion to the human summary.
// ok is false when the run was partial.
func BuildRunSummary(in SummaryInput, ok bool) RunSummary {
	rows := map[string]any{}
	for k, v := range in.Manifest.Counts {
		rows[k] = v
	}
	rows["_table_count"] = len(in.Manifest.Counts)

	partial := in.PartialFails
	if partial == nil {
		partial = []PartialFailure{}
	}

	return RunSummary{
		Kind:       "run_summary",
		OK:         ok,
		DurationS:  int(in.Duration.Round(time.Second).Seconds()),
		Artifact: SummaryArtifact{
			Path:          in.ArtifactPath,
			SizeBytes:     in.Size,
			SHA256:        in.SHA256,
			SchemaVersion: in.Manifest.SchemaVersion,
			LogPath:       in.LogPath,
		},
		Rows:       rows,
		Provenance: summarizeProvenance(in.Manifest.Provenance),
		Partial:    partial,
	}
}

// ExtractPartialFailures walks the manifest's provenance for non-empty
// Errors maps and returns one PartialFailure per (repo, connector). The
// reason is the first (sorted) error message — enough for the summary, the
// full set lives in manifest.json.
func ExtractPartialFailures(provs []connector.Provenance) []PartialFailure {
	var out []PartialFailure
	for _, p := range provs {
		if len(p.Errors) == 0 {
			continue
		}
		keys := make([]string, 0, len(p.Errors))
		for k := range p.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, PartialFailure{
			Repo:      p.Repo,
			Connector: p.Connector,
			Reason:    p.Errors[keys[0]],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Connector < out[j].Connector
	})
	return out
}

func writeRowsBlock(b *strings.Builder, counts map[string]int) {
	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(counts))
	totalTables := len(counts)
	for k, v := range counts {
		if v > 0 {
			rows = append(rows, row{k, v})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})

	fmt.Fprintln(b, "Rows captured")
	if len(rows) == 0 {
		fmt.Fprintln(b, "  (no rows captured)")
		if totalTables > 0 {
			fmt.Fprintf(b, "  (%d tables total)\n", totalTables)
		}
		return
	}

	shown := rows
	if len(shown) > summaryTopRows {
		shown = shown[:summaryTopRows]
	}

	width := 0
	for _, r := range shown {
		if len(r.name) > width {
			width = len(r.name)
		}
	}
	for _, r := range shown {
		fmt.Fprintf(b, "  %-*s  %s\n", width, r.name, humanInt(r.count))
	}
	if totalTables > len(shown) {
		fmt.Fprintf(b, "  (%d tables total)\n", totalTables)
	}
}

func writeProvenanceBlock(b *strings.Builder, p SummaryProvenance) {
	fmt.Fprintln(b, "Provenance")

	inaccessibleNote := ""
	if p.EndpointsInaccessible > 0 {
		inaccessibleNote = fmt.Sprintf(" (%d permission-gated, see manifest)", p.EndpointsInaccessible)
	}
	fmt.Fprintf(b, "  endpoints accessible:  %d/%d%s\n",
		p.EndpointsAccessible, p.EndpointsTotal, inaccessibleNote)

	errNote := ""
	if p.PerRowErrors > 0 {
		errNote = " (continued past, recorded in manifest)"
	}
	fmt.Fprintf(b, "  per-row errors:        %d%s\n", p.PerRowErrors, errNote)

	fmt.Fprintf(b, "  rate-limit truncated:  %d\n", p.RateLimitTruncated)
	fmt.Fprintf(b, "  partial paginations:   %d\n", p.PartialPaginations)
}

func summarizeProvenance(provs []connector.Provenance) SummaryProvenance {
	out := SummaryProvenance{}
	for _, p := range provs {
		for _, e := range p.Endpoints {
			out.EndpointsTotal++
			if e.Accessible {
				out.EndpointsAccessible++
			} else {
				out.EndpointsInaccessible++
			}
		}
		out.PerRowErrors += len(p.Errors)
		if p.RateLimitTruncated {
			out.RateLimitTruncated++
		}
		if !p.PaginationComplete {
			out.PartialPaginations++
		}
	}
	return out
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func formatSize(n int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func humanInt(n int) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

func lastPathSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
