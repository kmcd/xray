package progress

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONSink_PhaseStartWireShape(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	s.Emit(Event{
		Kind:      PhaseStart,
		Repo:      "kmcd/foo",
		Connector: "github",
		Phase:     "prs",
		At:        time.Date(2026, 6, 9, 14, 10, 23, 0, time.UTC),
	})
	got := strings.TrimSpace(buf.String())
	want := `{"ts":"2026-06-09T14:10:23Z","kind":"phase_start","repo":"kmcd/foo","connector":"github","phase":"prs"}`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

func TestJSONSink_PhaseProgressWireShape(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	s.Emit(Event{
		Kind:      PhaseProgress,
		Repo:      "kmcd/foo",
		Connector: "github",
		Phase:     "prs",
		Done:      47,
		Total:     120,
		At:        time.Date(2026, 6, 9, 14, 10, 45, 0, time.UTC),
	})
	got := strings.TrimSpace(buf.String())
	want := `{"ts":"2026-06-09T14:10:45Z","kind":"phase_progress","repo":"kmcd/foo","connector":"github","phase":"prs","done":47,"total":120}`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

func TestJSONSink_OmitEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)
	s.Emit(Event{Kind: PhaseDone, At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	got := strings.TrimSpace(buf.String())
	want := `{"ts":"2026-01-01T00:00:00Z","kind":"phase_done"}`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

func TestJSONSink_ConcurrentLinesAtomic(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONSink(&buf)

	const N = 200
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Emit(Event{
				Kind:      PhaseStart,
				Repo:      "r",
				Connector: "c",
				Phase:     "p",
				At:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			})
		}()
	}
	wg.Wait()

	scanner := bufio.NewScanner(&buf)
	lines := 0
	for scanner.Scan() {
		var w wireEvent
		if err := json.Unmarshal(scanner.Bytes(), &w); err != nil {
			t.Fatalf("line %d invalid JSON: %v\n%s", lines, err, scanner.Text())
		}
		lines++
	}
	if lines != N {
		t.Errorf("expected %d lines, got %d", N, lines)
	}
}
