package progress

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// TTYSink renders a live (repo × connector) status grid using
// hand-rolled ANSI cursor-up + clear-from-cursor redraws. Refresh runs
// at ~5 Hz from a background goroutine started by Start; concurrent
// Emit calls and the ticker are serialised by mu.
//
// The renderer never depends on terminal capabilities beyond CUU
// (cursor up) and ED (erase display from cursor); both are universally
// supported on the terminals xray targets.
type TTYSink struct {
	w       io.Writer
	mu      sync.Mutex
	cells   map[cellKey]*cell
	repos   []string // insertion-sorted
	conns   []string // insertion-sorted
	started time.Time
	workers int
	msg     string

	lastLines int

	stop chan struct{}
	done chan struct{}

	tick   time.Duration
	nowFn  func() time.Time
	closed bool
}

type cellKey struct{ repo, conn string }

type cellState int

const (
	cellPending cellState = iota
	cellRunning
	cellDone
	cellError
	cellSkipped
)

type cell struct {
	state   cellState
	started time.Time
	ended   time.Time
	rows    int64
	msg     string
}

// NewTTYSink wraps a writer (typically os.Stdout). Start must be called
// before any Emit for the grid to render incrementally; without Start,
// Emit still records state and a single render fires on Stop.
func NewTTYSink(w io.Writer) *TTYSink {
	return &TTYSink{
		w:     w,
		cells: map[cellKey]*cell{},
		tick:  200 * time.Millisecond,
		nowFn: func() time.Time { return time.Now() },
	}
}

// Plan pre-registers the full (repo × connector) grid so every cell
// renders as pending from the first frame. Without Plan, cells appear
// only as their PhaseStart events arrive — fine but less informative.
func (s *TTYSink) Plan(repos, connectors []string, workers int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workers = workers
	for _, r := range repos {
		s.insertRepo(r)
		for _, c := range connectors {
			s.insertConn(c)
			k := cellKey{r, c}
			if _, ok := s.cells[k]; !ok {
				s.cells[k] = &cell{state: cellPending}
			}
		}
	}
}

// Start launches the redraw ticker. Idempotent — repeat calls are no-ops.
// Cancellation via ctx or Stop both shut the ticker down; Stop also
// renders one final frame with finalised state.
func (s *TTYSink) Start(ctx context.Context) {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	s.started = s.nowFn()
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop := s.stop
	done := s.done
	tick := s.tick
	s.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-t.C:
				s.redraw()
			}
		}
	}()
}

// Stop shuts the ticker down and renders one final frame. Safe to call
// multiple times.
func (s *TTYSink) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	stop := s.stop
	done := s.done
	s.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	s.redraw()
	// Trailing newline so the subsequent stdout line (e.g. "wrote …")
	// appears below the grid rather than overwriting the last row.
	_, _ = fmt.Fprint(s.w, "\n")
}

// Emit records the event and (if Start has been called) lets the
// ticker pick it up on the next tick. Sub-tick latency is acceptable
// at 5 Hz and avoids redraw thrash under high-event-rate phases.
func (s *TTYSink) Emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch e.Kind {
	case RateLimit, Retry:
		if e.Message != "" {
			s.msg = e.Message
		}
		return
	}
	if e.Repo == "" || e.Connector == "" {
		// Phase=postprocess and other run-global events update the
		// rotating message line only.
		if e.Message != "" {
			s.msg = e.Message
		}
		return
	}
	s.insertRepo(e.Repo)
	s.insertConn(e.Connector)
	k := cellKey{e.Repo, e.Connector}
	c, ok := s.cells[k]
	if !ok {
		c = &cell{state: cellPending}
		s.cells[k] = c
	}
	now := s.nowFn()
	switch e.Kind {
	case PhaseStart:
		c.state = cellRunning
		c.started = now
		c.msg = e.Phase
	case PhaseProgress:
		c.state = cellRunning
		if e.Done > 0 {
			c.rows = e.Done
		}
	case PhaseDone:
		c.state = cellDone
		c.ended = now
		if e.Done > 0 {
			c.rows = e.Done
		}
	case PhaseError:
		c.state = cellError
		c.ended = now
		c.msg = e.Message
	case PhaseSkipped:
		c.state = cellSkipped
		c.ended = now
		c.msg = e.Message
	}
}

func (s *TTYSink) insertRepo(r string) {
	for _, x := range s.repos {
		if x == r {
			return
		}
	}
	s.repos = append(s.repos, r)
	sort.Strings(s.repos)
}

func (s *TTYSink) insertConn(c string) {
	for _, x := range s.conns {
		if x == c {
			return
		}
	}
	s.conns = append(s.conns, c)
	sort.Strings(s.conns)
}

// redraw moves the cursor up the height of the previous frame, clears
// from that point to the end of the screen, and writes the new frame.
func (s *TTYSink) redraw() {
	s.mu.Lock()
	frame := s.render(s.nowFn())
	prev := s.lastLines
	s.lastLines = strings.Count(frame, "\n")
	s.mu.Unlock()

	var b strings.Builder
	if prev > 0 {
		// CUU prev — move cursor up; ED 0 — clear to end of screen.
		fmt.Fprintf(&b, "\x1b[%dA\x1b[0J", prev)
	}
	b.WriteString(frame)
	_, _ = fmt.Fprint(s.w, b.String())
}

// render builds one frame for the current state. Pure relative to the
// sink's locked state, so tests drive it directly via a fixed nowFn.
func (s *TTYSink) render(now time.Time) string {
	if len(s.repos) == 0 && len(s.conns) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(s.headerLine(now))
	b.WriteString("\n\n")

	repoW := repoColumnWidth(s.repos)
	connW := connectorColumnWidth(s.conns)

	// Column header row.
	fmt.Fprintf(&b, "%-*s", repoW, "repo")
	for _, c := range s.conns {
		fmt.Fprintf(&b, "  %-*s", connW, c)
	}
	b.WriteString("\n")

	for _, r := range s.repos {
		fmt.Fprintf(&b, "%-*s", repoW, truncate(r, repoW))
		for _, c := range s.conns {
			cl := s.cells[cellKey{r, c}]
			fmt.Fprintf(&b, "  %-*s", connW, truncate(cellText(cl), connW))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s *TTYSink) headerLine(now time.Time) string {
	elapsed := time.Duration(0)
	if !s.started.IsZero() {
		elapsed = now.Sub(s.started).Round(time.Second)
	}
	eta := s.computeETA()
	parts := []string{"xray run", fmt.Sprintf("elapsed %s", formatHMS(elapsed))}
	if eta == nil {
		parts = append(parts, "ETA —")
	} else {
		band := time.Duration(float64(*eta) * 0.2)
		if band < time.Minute {
			band = time.Minute
		}
		parts = append(parts, fmt.Sprintf("ETA %s ±%dm", formatHMS(eta.Round(time.Second)), int(band.Minutes())))
	}
	if s.workers > 0 {
		running := 0
		for _, c := range s.cells {
			if c.state == cellRunning {
				running++
			}
		}
		parts = append(parts, fmt.Sprintf("%d/%d workers", running, s.workers))
	}
	if s.msg != "" {
		parts = append(parts, s.msg)
	}
	return strings.Join(parts, " · ")
}

// computeETA returns nil while no connector has any completion to learn
// from. With at least one completion per connector, ETA is sum over
// connectors of (mean(connector) × outstanding(connector)); divided by
// workers when workers > 0 to estimate parallel wall-clock.
func (s *TTYSink) computeETA() *time.Duration {
	type stat struct {
		total time.Duration
		count int
	}
	means := map[string]time.Duration{}
	collected := map[string]stat{}
	for k, c := range s.cells {
		if c.state == cellDone || c.state == cellError || c.state == cellSkipped {
			if !c.started.IsZero() && !c.ended.IsZero() {
				st := collected[k.conn]
				st.total += c.ended.Sub(c.started)
				st.count++
				collected[k.conn] = st
			}
		}
	}
	for conn, st := range collected {
		if st.count > 0 {
			means[conn] = st.total / time.Duration(st.count)
		}
	}
	var total time.Duration
	haveAny := false
	for _, c := range s.conns {
		mean, ok := means[c]
		if !ok {
			continue
		}
		outstanding := 0
		for _, r := range s.repos {
			cl := s.cells[cellKey{r, c}]
			if cl == nil || cl.state == cellPending || cl.state == cellRunning {
				outstanding++
			}
		}
		if outstanding > 0 {
			total += mean * time.Duration(outstanding)
			haveAny = true
		}
	}
	if !haveAny {
		return nil
	}
	if s.workers > 1 {
		total /= time.Duration(s.workers)
	}
	return &total
}

func cellText(c *cell) string {
	if c == nil {
		return "▢ pending"
	}
	switch c.state {
	case cellPending:
		return "▢ pending"
	case cellRunning:
		if c.msg != "" {
			return "● " + c.msg
		}
		return "● running"
	case cellDone:
		if c.rows > 0 {
			return fmt.Sprintf("✔ %d rows", c.rows)
		}
		return "✔ done"
	case cellError:
		return "✘ error"
	case cellSkipped:
		return "🔒 inaccessible"
	}
	return ""
}

func formatHMS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	sec := int(d % time.Minute / time.Second)
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

func repoColumnWidth(repos []string) int {
	w := len("repo")
	for _, r := range repos {
		if n := visualLen(r); n > w {
			w = n
		}
	}
	if w > 40 {
		w = 40
	}
	return w
}

func connectorColumnWidth(conns []string) int {
	w := 16
	for _, c := range conns {
		if n := visualLen(c); n > w {
			w = n
		}
	}
	// Wide enough for "✔ NNNN rows" or "● phase".
	if w < 18 {
		w = 18
	}
	return w
}

// visualLen approximates printed column width: counts runes rather than
// bytes so the column math accounts for multibyte connector names like
// "github_actions".
func visualLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func truncate(s string, w int) string {
	if visualLen(s) <= w {
		return s
	}
	out := []rune(s)
	if w <= 1 {
		return string(out[:w])
	}
	return string(out[:w-1]) + "…"
}
