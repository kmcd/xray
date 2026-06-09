package progress

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func newTestLogSink(verbose bool) (*LogSink, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return NewLogSink(slog.New(h), verbose), &buf
}

func TestLogSink_TransitionEmitted(t *testing.T) {
	s, buf := newTestLogSink(false)
	s.Emit(Event{
		Kind: PhaseStart, Repo: "kmcd/foo", Connector: "github", Phase: "prs",
	})
	out := buf.String()
	for _, want := range []string{
		"msg=progress", "kind=phase_start",
		"phase=prs", "repo=kmcd/foo", "connector=github",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestLogSink_ProgressDroppedWhenQuiet(t *testing.T) {
	s, buf := newTestLogSink(false)
	s.Emit(Event{Kind: PhaseProgress, Done: 47, Total: 120})
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %q", buf.String())
	}
}

func TestLogSink_ProgressKeptWhenVerbose(t *testing.T) {
	s, buf := newTestLogSink(true)
	s.Emit(Event{Kind: PhaseProgress, Repo: "r", Connector: "c", Done: 47, Total: 120})
	if !strings.Contains(buf.String(), "done=47") {
		t.Errorf("expected done=47 in %q", buf.String())
	}
	if !strings.Contains(buf.String(), "total=120") {
		t.Errorf("expected total=120 in %q", buf.String())
	}
}

func TestLogSink_ErrorUsesWarn(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	s := NewLogSink(slog.New(h), false)
	s.Emit(Event{Kind: PhaseError, Message: "boom"})
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected WARN level, got %q", buf.String())
	}
}

func TestLogSink_DoneEmitsRows(t *testing.T) {
	s, buf := newTestLogSink(false)
	s.Emit(Event{Kind: PhaseDone, Repo: "r", Connector: "c", Phase: "prs", Done: 4213})
	if !strings.Contains(buf.String(), "rows=4213") {
		t.Errorf("expected rows=4213 in %q", buf.String())
	}
}

func TestLogSink_ConcurrentEmitSafe(t *testing.T) {
	s, _ := newTestLogSink(false)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Emit(Event{Kind: PhaseStart, Repo: "r", Connector: "c", Phase: "p"})
		}()
	}
	wg.Wait()
}
