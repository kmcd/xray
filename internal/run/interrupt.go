package run

import (
	"sort"
	"sync"
)

// inflightTracker records (repo, connector) pairs that are currently
// executing inside the worker pool. Workers add themselves before calling
// Extract and remove themselves after, regardless of outcome. The
// dispatcher snapshots the map when ctx is canceled so the cmd layer's
// interrupt summary can name what was running at the moment the user
// pressed Ctrl-C.
type inflightTracker struct {
	mu sync.Mutex
	m  map[inflightKey]struct{}
}

type inflightKey struct {
	repo string
	conn string
}

func newInflightTracker() *inflightTracker {
	return &inflightTracker{m: map[inflightKey]struct{}{}}
}

func (t *inflightTracker) add(repo, conn string) {
	t.mu.Lock()
	t.m[inflightKey{repo, conn}] = struct{}{}
	t.mu.Unlock()
}

func (t *inflightTracker) done(repo, conn string) {
	t.mu.Lock()
	delete(t.m, inflightKey{repo, conn})
	t.mu.Unlock()
}

// snapshot returns the currently in-flight jobs sorted by (repo, connector)
// so the rendered summary is deterministic.
func (t *inflightTracker) snapshot() []InflightJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.m) == 0 {
		return nil
	}
	out := make([]InflightJob, 0, len(t.m))
	for k := range t.m {
		out = append(out, InflightJob{Repo: k.repo, Connector: k.conn})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Connector < out[j].Connector
	})
	return out
}

// interruptedResult builds the Result returned when ctx is canceled
// mid-run. TempDir is captured for the cmd layer's interrupt summary so
// the cleanup line can name the path (whether it was actually removed
// depends on KeepClones, which the cmd layer also knows about).
func interruptedResult(tmpDir, phase string, jobs []InflightJob) Result {
	return Result{
		Interrupted:      true,
		InterruptedPhase: phase,
		InflightJobs:     jobs,
		TempDir:          tmpDir,
	}
}
