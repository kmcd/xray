package progress

import (
	"testing"
	"time"
)

func TestRateLimitCounter_AccumulatesWaitDurationSeconds(t *testing.T) {
	c := NewRateLimitCounter()
	c.Emit(Event{Kind: RateLimit, At: time.Unix(1, 0), Fields: map[string]any{"wait_duration_s": 5}})
	c.Emit(Event{Kind: RateLimit, At: time.Unix(2, 0), Fields: map[string]any{"wait_duration_s": 10}})
	c.Emit(Event{Kind: RateLimit, At: time.Unix(3, 0), Fields: map[string]any{"wait_duration_s": 7}})

	waits, secs := c.Snapshot()
	if waits != 3 {
		t.Errorf("waits = %d, want 3", waits)
	}
	if secs != 22 {
		t.Errorf("totalSeconds = %d, want 22", secs)
	}
}

func TestRateLimitCounter_AcceptsMillisecondsFallback(t *testing.T) {
	c := NewRateLimitCounter()
	c.Emit(Event{Kind: RateLimit, Fields: map[string]any{"wait_duration_ms": 1500}})
	c.Emit(Event{Kind: RateLimit, Fields: map[string]any{"wait_duration_ms": 2500}})

	waits, secs := c.Snapshot()
	if waits != 2 {
		t.Errorf("waits = %d, want 2", waits)
	}
	if secs != 4 { // (1500 + 2500) / 1000 = 4
		t.Errorf("totalSeconds = %d, want 4", secs)
	}
}

func TestRateLimitCounter_IgnoresOtherEventKinds(t *testing.T) {
	c := NewRateLimitCounter()
	c.Emit(Event{Kind: PhaseStart, Fields: map[string]any{"wait_duration_s": 99}})
	c.Emit(Event{Kind: Retry, Fields: map[string]any{"wait_duration_s": 5}})
	c.Emit(Event{Kind: PhaseError})

	waits, secs := c.Snapshot()
	if waits != 0 || secs != 0 {
		t.Errorf("Snapshot = (%d, %d), want (0, 0); non-RateLimit events should be ignored", waits, secs)
	}
}

func TestRateLimitCounter_WaitCountsEvenWithoutDurationField(t *testing.T) {
	c := NewRateLimitCounter()
	c.Emit(Event{Kind: RateLimit, Fields: map[string]any{"reason": "secondary"}})
	c.Emit(Event{Kind: RateLimit})

	waits, secs := c.Snapshot()
	if waits != 2 {
		t.Errorf("waits = %d, want 2", waits)
	}
	if secs != 0 {
		t.Errorf("totalSeconds = %d, want 0 (no duration fields)", secs)
	}
}

func TestTeeSink_ForwardsToAllSinks(t *testing.T) {
	var a, b recordingSink
	tee := NewTeeSink(&a, nil, &b) // nil entry silently skipped
	tee.Emit(Event{Kind: PhaseStart, Repo: "r"})
	tee.Emit(Event{Kind: PhaseDone, Repo: "r"})

	if len(a.events) != 2 || len(b.events) != 2 {
		t.Errorf("len(a)=%d len(b)=%d, want both 2", len(a.events), len(b.events))
	}
	if a.events[0].Repo != "r" || b.events[1].Kind != PhaseDone {
		t.Errorf("events not forwarded faithfully: a=%+v b=%+v", a.events, b.events)
	}
}

type recordingSink struct{ events []Event }

func (r *recordingSink) Emit(e Event) { r.events = append(r.events, e) }
