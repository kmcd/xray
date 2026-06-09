package ratelimit

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BudgetState is the current X-RateLimit-* snapshot for one connector.
// Zero values mean "no header observed yet".
type BudgetState struct {
	Remaining int
	Limit     int
	ResetAt   time.Time
	UpdatedAt time.Time
}

type budgetTracker struct {
	mu sync.RWMutex
	m  map[string]BudgetState
}

func (b *budgetTracker) update(connector string, h http.Header, now time.Time) (BudgetState, bool) {
	remStr := h.Get("X-RateLimit-Remaining")
	limStr := h.Get("X-RateLimit-Limit")
	resetStr := h.Get("X-RateLimit-Reset")
	if remStr == "" && limStr == "" && resetStr == "" {
		return BudgetState{}, false
	}

	st := BudgetState{UpdatedAt: now}
	if v, err := strconv.Atoi(remStr); err == nil {
		st.Remaining = v
	}
	if v, err := strconv.Atoi(limStr); err == nil {
		st.Limit = v
	}
	if v, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
		st.ResetAt = time.Unix(v, 0).UTC()
	}

	b.mu.Lock()
	if b.m == nil {
		b.m = make(map[string]BudgetState)
	}
	b.m[connector] = st
	b.mu.Unlock()
	return st, true
}

func (b *budgetTracker) snapshot() map[string]BudgetState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]BudgetState, len(b.m))
	for k, v := range b.m {
		out[k] = v
	}
	return out
}

func (b *budgetTracker) get(connector string) (BudgetState, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	st, ok := b.m[connector]
	return st, ok
}

// hostToConnector maps an HTTP host to the connector name used as the
// key in Snapshot and in emitted progress events.
func hostToConnector(host string) string {
	h := strings.ToLower(host)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	switch {
	case h == "api.github.com", strings.HasSuffix(h, ".github.com"), h == "github.com":
		return "github"
	case strings.Contains(h, "sentry.io"), strings.HasSuffix(h, ".sentry.io"):
		return "sentry"
	case strings.Contains(h, "bugsnag.com"):
		return "bugsnag"
	case strings.Contains(h, "honeycomb.io"):
		return "honeycomb"
	case strings.Contains(h, "circleci.com"):
		return "circleci"
	}
	return h
}
