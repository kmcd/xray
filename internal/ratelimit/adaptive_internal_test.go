package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// These tests live in package ratelimit (not ratelimit_test) so they
// can reach escalateAdaptivePace and currentAdaptivePace directly —
// the decay model and the CAS-loop escalator can be exercised
// deterministically without going through Transport.RoundTrip and
// without waiting wall-clock for the 30-minute decay window.

// TestAdaptivePace_LadderStepsExactly verifies that successive
// escalateAdaptivePace calls advance the ladder through 500ms, 1s, 2s,
// 4s, 5s (capped) — matching the constants documented in ratelimit.go.
func TestAdaptivePace_LadderStepsExactly(t *testing.T) {
	tr := &Transport{}
	expected := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		adaptivePaceMax,
		adaptivePaceMax, // saturated
		adaptivePaceMax, // still saturated
	}
	for i, want := range expected {
		tr.escalateAdaptivePace()
		got := time.Duration(tr.adaptivePaceNanos.Load())
		if got != want {
			t.Errorf("step %d: ladder = %v, want %v", i+1, got, want)
		}
	}
}

// TestAdaptivePace_DecaysLinearlyOverWindow verifies the decay-on-read
// math: at trigger+0 the pace equals the ladder step; at
// trigger+decay/2 the pace is half; at trigger+decay the pace is zero.
// Uses a synthetic now to avoid wall-clock waits.
func TestAdaptivePace_DecaysLinearlyOverWindow(t *testing.T) {
	tr := &Transport{}
	tr.escalateAdaptivePace() // → 500ms
	tr.escalateAdaptivePace() // → 1s
	tr.escalateAdaptivePace() // → 2s
	base := time.Duration(tr.adaptivePaceNanos.Load())
	if base != 2*time.Second {
		t.Fatalf("setup: ladder = %v, want 2s", base)
	}
	trigger := time.Unix(0, tr.adaptiveTriggerNanos.Load())

	cases := []struct {
		name    string
		now     time.Time
		want    time.Duration
		tolNano int64
	}{
		{"at trigger", trigger, base, int64(time.Millisecond)},
		{"quarter decay", trigger.Add(adaptivePaceDecay / 4), base * 3 / 4, int64(10 * time.Millisecond)},
		{"half decay", trigger.Add(adaptivePaceDecay / 2), base / 2, int64(10 * time.Millisecond)},
		{"three-quarter decay", trigger.Add(adaptivePaceDecay * 3 / 4), base / 4, int64(10 * time.Millisecond)},
		{"at decay window", trigger.Add(adaptivePaceDecay), 0, 0},
		{"past decay window", trigger.Add(adaptivePaceDecay + time.Hour), 0, 0},
	}
	// Probes that cross the decay window zero out adaptivePaceNanos via
	// the CompareAndSwap in currentAdaptivePace, so re-prime between
	// probes that need a non-zero base. Order the cases small-elapsed
	// first to dodge that.
	for _, c := range cases {
		got := tr.currentAdaptivePace(c.now)
		diff := int64(got - c.want)
		if diff < 0 {
			diff = -diff
		}
		if diff > c.tolNano {
			t.Errorf("%s: pace = %v, want ~%v (±%v)", c.name, got, c.want, time.Duration(c.tolNano))
		}
	}
}

// TestAdaptivePace_ConcurrentEscalationsAdvanceLadder verifies the
// CAS loop in escalateAdaptivePace: N parallel callers each cause one
// step of advancement, so after N calls the ladder sits at the N-th
// step (or the cap, whichever is smaller). Without the CAS loop,
// concurrent writers would stomp each other and the ladder would
// effectively halt at step 1.
func TestAdaptivePace_ConcurrentEscalationsAdvanceLadder(t *testing.T) {
	tr := &Transport{}
	const goroutines = 32

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			tr.escalateAdaptivePace()
		}()
	}
	close(start)
	wg.Wait()

	// 32 escalations: 500ms, 1s, 2s, 4s, 5s, 5s, ... → saturates at max.
	got := time.Duration(tr.adaptivePaceNanos.Load())
	if got != adaptivePaceMax {
		t.Errorf("after %d concurrent escalations: ladder = %v, want %v (saturated at cap)", goroutines, got, adaptivePaceMax)
	}
	// adaptiveTriggerNanos must be set to a non-zero recent timestamp.
	if tr.adaptiveTriggerNanos.Load() == 0 {
		t.Error("adaptiveTriggerNanos not set after concurrent escalations")
	}
}

// TestAdaptivePace_ZeroStateReturnsZero pins down the no-trigger path:
// a fresh Transport reports zero pace and the decay-on-read math
// short-circuits before touching adaptiveTriggerNanos.
func TestAdaptivePace_ZeroStateReturnsZero(t *testing.T) {
	tr := &Transport{}
	if got := tr.currentAdaptivePace(time.Now()); got != 0 {
		t.Errorf("fresh transport: pace = %v, want 0", got)
	}
}

// TestAdaptivePace_RecoversFromCapWithinDecayWindow pins down the
// wall-clock behaviour required by issue #151: from a saturated ladder
// the pace must reach zero within adaptivePaceDecay, and must already
// be sub-second well before the window ends. v0.4.8 shipped a 30-minute
// decay window which mathematically worked but left long-window runs
// throttled past the planner-estimated wall-clock; v0.4.9 shortens the
// window to 3 minutes so a 3-minute storm costs ~3 minutes of recovery
// tail rather than ~30 minutes.
//
// Probes ordered small-elapsed first because each probe past the
// decay window returns 0 and a subsequent same-base probe would too;
// the small-elapsed ordering verifies the curve directly.
func TestAdaptivePace_RecoversFromCapWithinDecayWindow(t *testing.T) {
	tr := &Transport{}
	for i := 0; i < 10; i++ {
		tr.escalateAdaptivePace()
	}
	base := time.Duration(tr.adaptivePaceNanos.Load())
	if base != adaptivePaceMax {
		t.Fatalf("setup: ladder = %v, want %v (saturated)", base, adaptivePaceMax)
	}
	trigger := time.Unix(0, tr.adaptiveTriggerNanos.Load())

	cases := []struct {
		name   string
		now    time.Time
		assert func(t *testing.T, pace time.Duration)
	}{
		{
			name: "halfway through window: pace still above 1s but well below cap",
			now:  trigger.Add(adaptivePaceDecay / 2),
			assert: func(t *testing.T, pace time.Duration) {
				if pace < time.Second || pace > 3*time.Second {
					t.Errorf("pace at half-decay = %v, want in [1s, 3s]", pace)
				}
			},
		},
		{
			name: "at decay window: pace fully relaxed to zero",
			now:  trigger.Add(adaptivePaceDecay),
			assert: func(t *testing.T, pace time.Duration) {
				if pace != 0 {
					t.Errorf("pace at decay end = %v, want 0", pace)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.assert(t, tr.currentAdaptivePace(c.now))
		})
	}

	if adaptivePaceDecay > 5*time.Minute {
		t.Errorf("adaptivePaceDecay = %v: #151 requires recovery within minutes, not tens of minutes; revisit before relaxing this bound", adaptivePaceDecay)
	}
}
