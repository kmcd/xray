package progress

import (
	"context"
	"testing"
)

func TestFromContext_DefaultsToNopSinkWhenAbsent(t *testing.T) {
	s := FromContext(context.Background())
	if _, ok := s.(NopSink); !ok {
		t.Errorf("FromContext(empty) = %T, want NopSink", s)
	}
	// Nil ctx is also tolerated — callers can Emit unconditionally.
	if _, ok := FromContext(nil).(NopSink); !ok { //nolint:staticcheck // SA1012: explicitly testing the nil-ctx tolerance
		t.Errorf("FromContext(nil) did not return NopSink")
	}
}

func TestWithSink_RoundTrips(t *testing.T) {
	var rec recordingSink
	ctx := WithSink(context.Background(), &rec)

	got := FromContext(ctx)
	if got != &rec {
		t.Errorf("FromContext returned %p, want %p", got, &rec)
	}

	got.Emit(Event{Kind: PhaseStart, Repo: "r"})
	if len(rec.events) != 1 || rec.events[0].Repo != "r" {
		t.Errorf("sink not invoked through ctx: %+v", rec.events)
	}
}

func TestWithSink_NilSinkIsNoop(t *testing.T) {
	ctx := WithSink(context.Background(), nil)
	if _, ok := FromContext(ctx).(NopSink); !ok {
		t.Errorf("WithSink(nil) should leave NopSink as the effective sink")
	}
}
