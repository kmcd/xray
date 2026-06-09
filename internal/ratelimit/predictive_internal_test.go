package ratelimit

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/progress"
)

// These tests live in package ratelimit (not ratelimit_test) so they can
// reach Transport.startedAt and t.budgets directly — the predictive
// warning depends on a 5-minute elapsed gate that a black-box test
// can't observe without waiting in real time.

type capturingSink struct {
	mu sync.Mutex
	ev []progress.Event
}

func (c *capturingSink) Emit(e progress.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ev = append(c.ev, e)
}

func (c *capturingSink) phaseErrors() []progress.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []progress.Event
	for _, e := range c.ev {
		if e.Kind == progress.PhaseError {
			out = append(out, e)
		}
	}
	return out
}

// drive sets the elapsed run time and forces a budget state, then calls
// the predictive warning evaluator. Returns the events captured.
func driveWarning(t *Transport, sink progress.Sink, connector string, st BudgetState) {
	// Make startedAt look like 6 minutes ago so the 5-minute gate passes.
	t.startedAt.Store(time.Now().Add(-6 * time.Minute).UnixNano())
	t.budgets.mu.Lock()
	if t.budgets.m == nil {
		t.budgets.m = make(map[string]BudgetState)
	}
	t.budgets.m[connector] = st
	t.budgets.mu.Unlock()
	t.maybeEmitPredictiveWarning(sink, connector)
}

func TestPredictiveWarning_SkipsWhenHasRemainingFalse(t *testing.T) {
	tr := &Transport{Base: http.DefaultTransport}
	sink := &capturingSink{}

	// Remaining=0 but HasRemaining=false means the response carried only
	// Reset/Limit headers; treating Remaining=0 as authoritative would
	// fire a false-positive warning.
	driveWarning(tr, sink, "github", BudgetState{
		HasRemaining: false,
		Remaining:    0,
		Limit:        5000,
		ResetAt:      time.Now().Add(30 * time.Minute),
	})

	if got := sink.phaseErrors(); len(got) != 0 {
		t.Errorf("emitted %d PhaseError event(s), want 0; predictive warning should skip when HasRemaining is false", len(got))
	}
}

func TestPredictiveWarning_ClearsWarnedOnRecovery(t *testing.T) {
	tr := &Transport{Base: http.DefaultTransport}
	sink := &capturingSink{}

	low := BudgetState{HasRemaining: true, Remaining: 50, Limit: 5000, ResetAt: time.Now().Add(time.Hour)}
	hi := BudgetState{HasRemaining: true, Remaining: 4500, Limit: 5000, ResetAt: time.Now().Add(time.Hour)}

	// Dip 1 → expect one PhaseError.
	driveWarning(tr, sink, "github", low)
	if got := len(sink.phaseErrors()); got != 1 {
		t.Fatalf("after first dip: emitted %d, want 1", got)
	}

	// Same dip again (latch held) → still one.
	driveWarning(tr, sink, "github", low)
	if got := len(sink.phaseErrors()); got != 1 {
		t.Fatalf("after latched dip: emitted %d, want still 1", got)
	}

	// Recovery above predictClearAbove clears the latch.
	driveWarning(tr, sink, "github", hi)
	if got := len(sink.phaseErrors()); got != 1 {
		t.Fatalf("after recovery: emitted %d, want still 1 (recovery itself does not emit)", got)
	}

	// Subsequent dip should fire a second PhaseError.
	driveWarning(tr, sink, "github", low)
	if got := len(sink.phaseErrors()); got != 2 {
		t.Fatalf("after second dip: emitted %d, want 2 — warned latch did not clear on recovery", got)
	}
}

func TestPredictiveWarning_DoesNotFireBeforeFiveMinutes(t *testing.T) {
	tr := &Transport{Base: http.DefaultTransport}
	sink := &capturingSink{}

	// Set startedAt to 3 minutes ago — below the 5-minute gate.
	tr.startedAt.Store(time.Now().Add(-3 * time.Minute).UnixNano())
	tr.budgets.mu.Lock()
	tr.budgets.m = map[string]BudgetState{
		"github": {HasRemaining: true, Remaining: 10, Limit: 5000, ResetAt: time.Now().Add(time.Hour)},
	}
	tr.budgets.mu.Unlock()

	tr.maybeEmitPredictiveWarning(sink, "github")
	if got := sink.phaseErrors(); len(got) != 0 {
		t.Errorf("emitted %d PhaseError event(s), want 0 (5-minute elapsed gate)", len(got))
	}
}
