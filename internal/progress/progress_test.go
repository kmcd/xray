package progress

import "testing"

func TestEventIsTransition(t *testing.T) {
	cases := []struct {
		kind EventKind
		want bool
	}{
		{PhaseStart, true},
		{PhaseProgress, false},
		{PhaseDone, true},
		{PhaseError, true},
		{PhaseSkipped, true},
		{RateLimit, true},
		{Retry, true},
	}
	for _, tc := range cases {
		got := Event{Kind: tc.kind}.IsTransition()
		if got != tc.want {
			t.Errorf("IsTransition(%s) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestNopSink_EmitNeverPanics(t *testing.T) {
	var s NopSink
	s.Emit(Event{Kind: PhaseStart})
	s.Emit(Event{Kind: PhaseProgress, Done: 1, Total: 2})
	s.Emit(Event{}) // zero value
}
