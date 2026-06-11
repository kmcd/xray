// Package progress defines the structured run-time event contract for
// xray's CLI output cluster (cli-ux). A Sink consumes Events emitted at
// phase boundaries during a run; concrete sinks include a no-op default,
// a slog wrapper for the non-TTY log fallback, an NDJSON emitter for
// --output json, and a TTY status grid for --output auto on a terminal.
//
// The contract is the seam imported by sibling cli-ux work: rate-limit
// and retry visibility (#82), the post-run summary block (#84), and the
// run-time status display itself (#81). The package depends on the
// standard library only; concrete sinks may pull in additional helpers.
package progress

import "time"

// Sink consumes events emitted during a run. Implementations must be
// safe to call from multiple goroutines.
type Sink interface {
	Emit(Event)
}

// EventKind names a discrete run-time event. The string values are the
// wire shape used by --output json and are stable per the schema
// documented in docs/spec.md ("JSON event schema").
type EventKind string

const (
	PhaseStart    EventKind = "phase_start"
	PhaseProgress EventKind = "phase_progress"
	PhaseDone     EventKind = "phase_done"
	PhaseError    EventKind = "phase_error"
	PhaseSkipped  EventKind = "phase_skipped"
	RateLimit     EventKind = "rate_limit_wait"
	Retry         EventKind = "retry"
)

// Event is one run-time progress signal. Repo/Connector/Phase identify
// the (repo, connector) pair the event applies to; global events leave
// them empty. Done/Total advance on PhaseProgress; sinks that don't
// know a Total render the partial count alone.
type Event struct {
	Kind        EventKind
	Repo        string
	Connector   string
	Phase       string
	Done, Total int64
	Message     string
	At          time.Time
	Fields      map[string]any
}

// IsTransition reports whether an event marks a state transition rather
// than a mid-walk tick. Sinks that throttle output (LogSink without
// verbose, the TTY header) use this to keep noise down.
func (e Event) IsTransition() bool {
	return e.Kind != PhaseProgress
}

// BudgetEntry is a per-connector rate-limit budget snapshot. It mirrors
// ratelimit.BudgetState to avoid a circular import (ratelimit imports progress).
type BudgetEntry struct {
	Remaining    int
	HasRemaining bool
	Limit        int
	ResetAt      time.Time
}

// BudgetSource is a function that returns the current rate-limit budget
// per connector name. TTYSink calls it on each redraw tick. Nil when no
// transport reference is available (non-TTY modes never set it).
type BudgetSource func() map[string]BudgetEntry
