package github

import (
	"log/slog"
	"time"
)

// progress is a small helper for long-running github connector stages.
// It emits an info-level log line every progressChunk records OR every
// progressInterval, whichever first. The goal is to make "is it stuck?"
// answerable from the run log alone — without lsof / pgrep / sample —
// during multi-minute extractions on large repos.
//
// Stages instrumented today: commits, PRs, reviews, PR comments,
// file_metrics, harness_artifacts.
type progress struct {
	log      *slog.Logger
	repo     string
	stage    string
	count    int
	lastLog  time.Time
	started  time.Time
	chunk    int
	interval time.Duration
}

const (
	progressChunk    = 100
	progressInterval = 30 * time.Second
)

// newProgress constructs a progress tracker for a (repo, stage) pair.
// log may be nil; the tracker becomes a no-op.
func newProgress(log *slog.Logger, repo, stage string) *progress {
	now := time.Now()
	return &progress{
		log:      log,
		repo:     repo,
		stage:    stage,
		started:  now,
		lastLog:  now,
		chunk:    progressChunk,
		interval: progressInterval,
	}
}

// tick records one unit of progress. Emits a checkpoint log line when the
// count crosses a chunk boundary or the interval elapses.
func (p *progress) tick() {
	if p == nil || p.log == nil {
		return
	}
	p.count++
	if p.count%p.chunk == 0 || time.Since(p.lastLog) >= p.interval {
		p.log.Info("github: progress",
			slog.String("repo", p.repo),
			slog.String("stage", p.stage),
			slog.Int("count", p.count),
			slog.Duration("elapsed", time.Since(p.started)),
		)
		p.lastLog = time.Now()
	}
}

// done emits a final summary line. Safe to call even if no ticks fired.
func (p *progress) done() {
	if p == nil || p.log == nil {
		return
	}
	p.log.Info("github: progress complete",
		slog.String("repo", p.repo),
		slog.String("stage", p.stage),
		slog.Int("total", p.count),
		slog.Duration("elapsed", time.Since(p.started)),
	)
}
