package progress

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func newTestTTY() (*TTYSink, *bytes.Buffer) {
	var buf bytes.Buffer
	s := NewTTYSink(&buf)
	return s, &buf
}

func TestTTYSink_RenderPendingGrid(t *testing.T) {
	s, _ := newTestTTY()
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }
	s.Plan([]string{"kmcd/foo", "kmcd/bar"}, []string{"github", "sentry"}, 4)
	s.started = now

	out := s.render(now)
	for _, want := range []string{
		"xray run", "elapsed 00:00", "ETA —",
		"repo", "github", "sentry",
		"kmcd/bar", "▢ pending", "kmcd/foo",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
}

func TestTTYSink_TransitionsDrivenByEmit(t *testing.T) {
	s, _ := newTestTTY()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return t0 }
	s.Plan([]string{"kmcd/foo"}, []string{"github"}, 1)
	s.started = t0

	s.nowFn = func() time.Time { return t0.Add(10 * time.Second) }
	s.Emit(Event{Kind: PhaseStart, Repo: "kmcd/foo", Connector: "github", Phase: "prs"})

	out := s.render(s.nowFn())
	if !strings.Contains(out, "● prs") {
		t.Errorf("expected running cell with phase, got:\n%s", out)
	}

	s.nowFn = func() time.Time { return t0.Add(30 * time.Second) }
	s.Emit(Event{Kind: PhaseDone, Repo: "kmcd/foo", Connector: "github", Phase: "prs", Done: 4213})

	out = s.render(s.nowFn())
	if !strings.Contains(out, "✔ 4213 rows") {
		t.Errorf("expected done cell with row count, got:\n%s", out)
	}
}

func TestTTYSink_ErrorAndSkippedSymbols(t *testing.T) {
	s, _ := newTestTTY()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return t0 }
	s.Plan([]string{"a/b"}, []string{"github", "sentry"}, 1)
	s.started = t0

	s.Emit(Event{Kind: PhaseError, Repo: "a/b", Connector: "github", Message: "boom"})
	s.Emit(Event{Kind: PhaseSkipped, Repo: "a/b", Connector: "sentry"})

	out := s.render(t0)
	if !strings.Contains(out, "✘ error") {
		t.Errorf("missing error symbol in:\n%s", out)
	}
	if !strings.Contains(out, "🔒 inaccessible") {
		t.Errorf("missing skipped symbol in:\n%s", out)
	}
}

func TestTTYSink_ETARendersAfterCompletion(t *testing.T) {
	s, _ := newTestTTY()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return t0 }
	s.Plan([]string{"a/b", "c/d"}, []string{"github"}, 1)
	s.started = t0

	// First repo completes in 60s.
	s.nowFn = func() time.Time { return t0 }
	s.Emit(Event{Kind: PhaseStart, Repo: "a/b", Connector: "github"})
	s.nowFn = func() time.Time { return t0.Add(60 * time.Second) }
	s.Emit(Event{Kind: PhaseDone, Repo: "a/b", Connector: "github", Done: 100})

	now := t0.Add(60 * time.Second)
	out := s.render(now)
	if !strings.Contains(out, "ETA 01:00") {
		t.Errorf("expected ETA ~01:00 (one outstanding × 60s mean), got:\n%s", out)
	}
}

func TestTTYSink_HeaderRotatesMessageOnRateLimit(t *testing.T) {
	s, _ := newTestTTY()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return t0 }
	s.Plan([]string{"r"}, []string{"github"}, 1)
	s.started = t0

	s.Emit(Event{Kind: RateLimit, Message: "rate limited, waiting 12s"})
	out := s.render(t0)
	if !strings.Contains(out, "rate limited, waiting 12s") {
		t.Errorf("expected rate-limit message in header, got:\n%s", out)
	}
}

func TestTTYSink_RedrawEmitsCursorUp(t *testing.T) {
	s, buf := newTestTTY()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return t0 }
	s.Plan([]string{"r"}, []string{"github"}, 1)
	s.started = t0

	s.redraw()
	firstLen := buf.Len()
	if firstLen == 0 {
		t.Fatal("expected initial frame to be non-empty")
	}
	if strings.Contains(buf.String(), "\x1b[") && !strings.HasPrefix(buf.String(), "xray run") {
		t.Errorf("first frame should not start with cursor-up: %q", buf.String()[:20])
	}

	s.redraw()
	second := buf.String()[firstLen:]
	if !strings.Contains(second, "\x1b[") {
		t.Errorf("subsequent frame should include ANSI escapes, got:\n%q", second)
	}
}

func TestTTYSink_StartStopLifecycle(t *testing.T) {
	s, buf := newTestTTY()
	s.tick = 10 * time.Millisecond
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return t0 }
	s.Plan([]string{"r"}, []string{"github"}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	time.Sleep(40 * time.Millisecond)
	s.Stop()

	if buf.Len() == 0 {
		t.Errorf("expected output after ticker fired")
	}

	// Stop is idempotent.
	s.Stop()
}

func TestFormatHMS(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00"},
		{59 * time.Second, "00:59"},
		{61 * time.Second, "01:01"},
		{time.Hour + 2*time.Minute + 3*time.Second, "01:02:03"},
		{-time.Second, "00:00"},
	}
	for _, c := range cases {
		got := formatHMS(c.d)
		if got != c.want {
			t.Errorf("formatHMS(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 10) != "short" {
		t.Errorf("short passthrough failed")
	}
	if got := truncate("supercalifragilistic", 10); got != "supercali…" {
		t.Errorf("truncate long got %q", got)
	}
}

func TestHumanResetTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		reset time.Time
		want  string
	}{
		{time.Time{}, "resets —"},
		{now.Add(-time.Second), "resets now"},
		{now, "resets now"},
		{now.Add(45 * time.Second), "resets 45s"},
		{now.Add(28 * time.Minute), "resets 28m"},
		{now.Add(4 * time.Minute), "resets 4m"},
		{now.Add(90 * time.Minute), "resets 1h30m"},
		{now.Add(65 * time.Minute), "resets 1h05m"},
	}
	for _, c := range cases {
		got := humanResetTTL(c.reset, now)
		if got != c.want {
			t.Errorf("humanResetTTL(%v) = %q, want %q", c.reset, got, c.want)
		}
	}
}

func TestTTYSink_RenderBudgets_Basic(t *testing.T) {
	s, _ := newTestTTY()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }
	s.Plan([]string{"a/b"}, []string{"github", "sentry"}, 1)
	s.started = now
	s.SetBudgetSource(func() map[string]BudgetEntry {
		return map[string]BudgetEntry{
			"github": {Remaining: 4213, HasRemaining: true, Limit: 5000, ResetAt: now.Add(28 * time.Minute)},
			"sentry": {Remaining: 870, HasRemaining: true, Limit: 1000, ResetAt: now.Add(4 * time.Minute)},
		}
	})

	out := s.render(now)
	for _, want := range []string{
		"github", "4213/5000", "resets 28m",
		"sentry", "870/1000", "resets 4m",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
	// github must appear before sentry (sorted)
	if gi, si := strings.Index(out, "4213/5000"), strings.Index(out, "870/1000"); gi > si {
		t.Errorf("expected github before sentry in budget section")
	}
}

func TestTTYSink_RenderBudgets_Empty(t *testing.T) {
	s, _ := newTestTTY()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }
	s.Plan([]string{"a/b"}, []string{"github"}, 1)
	s.started = now

	// No budget source set — frame must be identical to baseline.
	baseline := s.render(now)
	s.SetBudgetSource(func() map[string]BudgetEntry { return nil })
	withNil := s.render(now)
	s.SetBudgetSource(func() map[string]BudgetEntry { return map[string]BudgetEntry{} })
	withEmpty := s.render(now)

	if baseline != withNil {
		t.Errorf("nil source changed render output")
	}
	if baseline != withEmpty {
		t.Errorf("empty source changed render output")
	}
}

func TestTTYSink_RenderBudgets_PartialFields(t *testing.T) {
	s, _ := newTestTTY()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }
	s.Plan([]string{"a/b"}, []string{"github"}, 1)
	s.started = now
	s.SetBudgetSource(func() map[string]BudgetEntry {
		return map[string]BudgetEntry{
			"github": {HasRemaining: false, Limit: 5000, ResetAt: time.Time{}},
		}
	})

	out := s.render(now)
	if !strings.Contains(out, "?/5000") {
		t.Errorf("expected ?/5000 for HasRemaining=false, got:\n%s", out)
	}
	if !strings.Contains(out, "resets —") {
		t.Errorf("expected 'resets —' for zero ResetAt, got:\n%s", out)
	}
}

func TestTTYSink_RenderBudgets_ResetInPast(t *testing.T) {
	s, _ := newTestTTY()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }
	s.Plan([]string{"a/b"}, []string{"github"}, 1)
	s.started = now
	s.SetBudgetSource(func() map[string]BudgetEntry {
		return map[string]BudgetEntry{
			"github": {Remaining: 100, HasRemaining: true, Limit: 5000, ResetAt: now.Add(-30 * time.Second)},
		}
	})

	out := s.render(now)
	if !strings.Contains(out, "resets now") {
		t.Errorf("expected 'resets now' for past ResetAt, got:\n%s", out)
	}
}

func TestTTYSink_RenderBudgets_SkipsNoInfo(t *testing.T) {
	s, _ := newTestTTY()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }
	s.Plan([]string{"a/b"}, []string{"github"}, 1)
	s.started = now

	baseline := s.render(now)
	s.SetBudgetSource(func() map[string]BudgetEntry {
		// Entry with no useful data — all zero.
		return map[string]BudgetEntry{
			"github": {HasRemaining: false, Limit: 0, ResetAt: time.Time{}},
		}
	})
	out := s.render(now)
	if baseline != out {
		t.Errorf("all-zero budget entry changed render output:\n%s", out)
	}
}
