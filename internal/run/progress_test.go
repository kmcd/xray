package run_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/progress"
	"github.com/kmcd/xray/internal/run"
)

type recordingSink struct {
	mu     sync.Mutex
	events []progress.Event
}

func (r *recordingSink) Emit(e progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingSink) snapshot() []progress.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]progress.Event, len(r.events))
	copy(out, r.events)
	return out
}

// TestRun_EmitsPhaseEventsThroughSink runs the standard stub-connector
// end-to-end path and asserts the sink receives PhaseStart/PhaseDone
// events for clone, extract, and postprocess phases.
func TestRun_EmitsPhaseEventsThroughSink(t *testing.T) {
	slug := "owner/fixture"
	bare := setupFakeRemote(t)
	installInsteadOf(t, map[string]string{slug: bare})

	stub := &stubConnector{name: "stub", commits: 1, prs: 1}
	sink := &recordingSink{}

	cfg := standardCfg(slug)
	out := filepath.Join(t.TempDir(), "artifact.tar.gz")
	opts := run.Options{
		Out:         out,
		ToolVersion: "test",
		Logger:      run.NewLogger(false, true),
		Connectors:  []connector.Connector{stub},
		Progress:    sink,
	}
	if _, err := run.Run(context.Background(), cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := sink.snapshot()
	have := func(kind progress.EventKind, conn, phase string) bool {
		for _, e := range events {
			if e.Kind == kind && e.Connector == conn && e.Phase == phase {
				return true
			}
		}
		return false
	}

	if !have(progress.PhaseStart, "clone", "clone") {
		t.Errorf("missing clone phase_start; got %d events", len(events))
	}
	if !have(progress.PhaseDone, "clone", "clone") {
		t.Errorf("missing clone phase_done")
	}
	if !have(progress.PhaseStart, "stub", "stub") {
		t.Errorf("missing extract phase_start for stub connector")
	}
	if !have(progress.PhaseDone, "stub", "stub") {
		t.Errorf("missing extract phase_done for stub connector")
	}

	hasPostprocessStart := false
	hasPostprocessDone := false
	for _, e := range events {
		if e.Phase == "postprocess" {
			switch e.Kind {
			case progress.PhaseStart:
				hasPostprocessStart = true
			case progress.PhaseDone:
				hasPostprocessDone = true
			}
		}
	}
	if !hasPostprocessStart {
		t.Errorf("missing postprocess phase_start")
	}
	if !hasPostprocessDone {
		t.Errorf("missing postprocess phase_done")
	}

	// PhaseDone for the stub connector should carry a non-zero row count
	// (commits + prs).
	for _, e := range events {
		if e.Kind == progress.PhaseDone && e.Connector == "stub" {
			if e.Done == 0 {
				t.Errorf("stub PhaseDone Done=0, expected >0")
			}
		}
	}
}
