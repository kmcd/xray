package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Mode is the resolved --output mode for a run. Other cli-ux issues (#81–#86)
// branch on this enum without re-deciding the semantics here.
type Mode int

const (
	// ModeAuto renders the status grid on a TTY (#81) and falls back to a
	// line-based stderr log on non-TTY. Today, before #81 lands, ModeAuto
	// behaves identically to ModeLog.
	ModeAuto Mode = iota
	// ModeQuiet emits only the artifact path to stdout on success and
	// errors to stderr. All other progress output is suppressed.
	ModeQuiet
	// ModeJSON emits one JSON object per progress event to stdout, with a
	// final {"kind":"summary",...} object. The event schema is documented
	// in docs/spec.md and is versioned independently of the artifact
	// SchemaVersion.
	ModeJSON
	// ModeLog forces the line-based stderr log even on a TTY.
	ModeLog
)

func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeQuiet:
		return "quiet"
	case ModeJSON:
		return "json"
	case ModeLog:
		return "log"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// ParseMode parses the value of the --output flag. The empty string is
// treated as "auto" so callers can pass an unset flag through.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", "auto":
		return ModeAuto, nil
	case "quiet":
		return ModeQuiet, nil
	case "json":
		return ModeJSON, nil
	case "log":
		return ModeLog, nil
	default:
		return ModeAuto, fmt.Errorf("invalid --output value %q (want auto|quiet|json|log)", s)
	}
}

// ResolveMode combines the --output flag value and the legacy --quiet bool
// into a single Mode. --quiet is a shorthand for --output quiet; setting
// both with conflicting values is a flag-level error.
func ResolveMode(outputFlag string, quietFlag bool) (Mode, error) {
	if quietFlag && outputFlag != "" && outputFlag != "quiet" {
		return ModeAuto, fmt.Errorf("--quiet conflicts with --output %s", outputFlag)
	}
	if quietFlag {
		return ModeQuiet, nil
	}
	return ParseMode(outputFlag)
}

// IsTTY reports whether f is attached to a character device (terminal).
// Used by ModeAuto consumers (#81) to choose between the status grid and
// the line-based log.
func IsTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}

// SummaryEvent is the final JSON object emitted in ModeJSON. The wire
// shape is part of the documented output schema (docs/spec.md); additive
// changes are non-breaking.
type SummaryEvent struct {
	TS       string         `json:"ts"`
	Kind     string         `json:"kind"`
	Artifact string         `json:"artifact"`
	SHA256   string         `json:"sha256,omitempty"`
	Rows     map[string]int `json:"rows,omitempty"`
	ExitCode int            `json:"exit_code"`
}

// EmitSummary writes one SummaryEvent to w as a single line of NDJSON.
// Kind is forced to "summary" regardless of the input value to keep the
// wire shape stable.
func EmitSummary(w io.Writer, ev SummaryEvent) error {
	ev.Kind = "summary"
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(ev)
}
