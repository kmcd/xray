package progress

import "sync"

// RateLimitCounter accumulates RateLimit events emitted by
// ratelimit.Transport so the post-run summary can report the cumulative
// wait count + total seconds. It implements Sink and is intended to be
// tee'd alongside the run's primary Sink (e.g. via NewTeeSink).
type RateLimitCounter struct {
	mu      sync.Mutex
	waits   int
	totalMS int64
}

// NewRateLimitCounter returns a zero-value counter ready to consume
// events.
func NewRateLimitCounter() *RateLimitCounter { return &RateLimitCounter{} }

// Emit ignores every event except Kind == RateLimit. The wait duration
// is read from Fields["wait_duration_s"] (int seconds) or
// Fields["wait_duration_ms"] (int ms) if present; events without either
// contribute to the wait count but not the duration.
func (c *RateLimitCounter) Emit(e Event) {
	if e.Kind != RateLimit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits++
	if v, ok := e.Fields["wait_duration_s"].(int); ok {
		c.totalMS += int64(v) * 1000
		return
	}
	if v, ok := e.Fields["wait_duration_ms"].(int); ok {
		c.totalMS += int64(v)
	}
}

// Snapshot returns the cumulative counters. Safe to call concurrently
// with Emit.
func (c *RateLimitCounter) Snapshot() (waits int, totalSeconds int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waits, int(c.totalMS / 1000)
}

// TeeSink fans out a single Emit call to every wrapped sink. Used in
// the CLI to tee a RateLimitCounter alongside the user-facing TTY or
// log sink without coupling either to the other.
type TeeSink struct {
	sinks []Sink
}

// NewTeeSink returns a sink that forwards every Emit to each of sinks
// in order. Nil entries are silently skipped.
func NewTeeSink(sinks ...Sink) *TeeSink {
	out := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		if s != nil {
			out = append(out, s)
		}
	}
	return &TeeSink{sinks: out}
}

// Emit forwards e to every wrapped sink. Implementations are expected
// to be cheap and non-blocking; TeeSink itself does not goroutinize.
func (t *TeeSink) Emit(e Event) {
	for _, s := range t.sinks {
		s.Emit(e)
	}
}
