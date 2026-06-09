package ratelimit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/progress"
	"github.com/kmcd/xray/internal/ratelimit"
)

type recordingSink struct {
	mu     sync.Mutex
	events []progress.Event
}

func (s *recordingSink) Emit(ev progress.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *recordingSink) byKind(k progress.EventKind) []progress.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []progress.Event
	for _, ev := range s.events {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

// mkRespBody is like mkResp but lets the test attach a body the
// secondary-rate-limit detector can read.
func mkRespBody(status int, headers map[string]string, body string) *http.Response {
	r := mkResp(status, headers)
	r.Body = io.NopCloser(bytes.NewReader([]byte(body)))
	return r
}

func mkResp(status int, headers map[string]string) *http.Response {
	r := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       http.NoBody,
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestRetryOn429WithRetryAfter(t *testing.T) {
	var calls int32
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return mkResp(429, map[string]string{"Retry-After": "0"}), nil
		}
		return mkResp(200, nil), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls: got %d want 2", got)
	}
}

func TestRetryOn5xx(t *testing.T) {
	var calls int32
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			return mkResp(503, map[string]string{"Retry-After": "0"}), nil
		}
		return mkResp(200, nil), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls: got %d want 3", got)
	}
}

// GitHub's secondary (anti-burst) rate limit returns 403 with a JSON body
// whose message contains "secondary rate limit". The transport must
// recognise it and retry, since the underlying token is fine — only the
// burst rate was exceeded.
func TestRetryOnSecondaryRateLimit(t *testing.T) {
	const body = `{"documentation_url":"https://docs.github.com/graphql/overview/rate-limits-and-node-limits-for-the-graphql-api#secondary-rate-limits","message":"You have exceeded a secondary rate limit."}`
	var calls int32
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return mkRespBody(403, map[string]string{"Retry-After": "0"}, body), nil
		}
		return mkResp(200, nil), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

// A 403 with no rate-limit signature stays a permanent failure (a real
// permission error, not throttling).
func TestNoRetryOnPlain403(t *testing.T) {
	var calls int32
	fn := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return mkRespBody(403, nil, `{"message":"Forbidden"}`), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("final status = %d, want 403", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on permanent 403)", got)
	}
}

// Secondary-RL waits charge against SecondaryRateLimitBudget; transient
// (429 / 5xx) waits charge against CumulativeBudget. The split lets the
// caller exhaust one without exhausting the other.
func TestPerErrorClassBudgets(t *testing.T) {
	const body = `{"message":"You have exceeded a secondary rate limit"}`
	// First call: 429 (transient). Second: secondary-RL 403. Third: 200.
	var calls int32
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		switch n {
		case 1:
			return mkResp(429, map[string]string{"Retry-After": "0"}), nil
		case 2:
			return mkRespBody(403, map[string]string{"Retry-After": "0"}, body), nil
		default:
			return mkResp(200, nil), nil
		}
	}
	// Tight transient budget, generous secondary-RL budget. If they
	// shared a single counter, the secondary-RL retry would push us over
	// the 1s CumulativeBudget. With per-class accounting both retries fit.
	p := ratelimit.Policy{
		MaxAttempts:              5,
		CumulativeBudget:         1 * time.Second,
		SecondaryRateLimitBudget: 5 * time.Second,
	}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

// Body is re-attached after the secondary-rate-limit detector peeks at
// it, so the terminal-attempt caller can still read the full error.
func TestSecondaryRateLimitBodyReattached(t *testing.T) {
	const body = `{"message":"You have exceeded a secondary rate limit."}`
	var calls int32
	fn := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		// Always 403 — exhaust the retry budget.
		return mkRespBody(403, map[string]string{"Retry-After": "0"}, body), nil
	}
	p := ratelimit.Policy{MaxAttempts: 2, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != body {
		t.Errorf("body after peek = %q, want %q", string(got), body)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls int32
	fn := func() (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return mkResp(400, nil), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls: got %d want 1", got)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fn := func() (*http.Response, error) {
		// Force a long backoff via Retry-After.
		return mkResp(429, map[string]string{"Retry-After": "60"}), nil
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Minute}
	resp, err := ratelimit.Do(ctx, p, nil, fn)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatalf("expected error on cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err: got %v want context.Canceled", err)
	}
}

func TestXRateLimitReset(t *testing.T) {
	var calls int32
	resetAt := strconv.FormatInt(time.Now().Add(0*time.Second).Unix(), 10)
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return mkResp(429, map[string]string{"X-RateLimit-Reset": resetAt}), nil
		}
		return mkResp(200, nil), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

func TestNetworkErrorRetries(t *testing.T) {
	var calls int32
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			return nil, io.ErrUnexpectedEOF
		}
		return mkResp(200, nil), nil
	}
	p := ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second}
	resp, err := ratelimit.Do(context.Background(), p, nil, fn)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

// TestLowWaterMarkSleep verifies that Transport sleeps before the next request
// when X-RateLimit-Remaining falls below LowWaterMark on the prior response.
// The triggering response is returned immediately; the sleep fires at the
// start of the subsequent RoundTrip call.
func TestLowWaterMarkSleep(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Second)
	resetUnix := strconv.FormatInt(resetAt.Unix(), 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "5")
		w.Header().Set("X-RateLimit-Reset", resetUnix)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &ratelimit.Transport{
		Base:   http.DefaultTransport,
		Policy: ratelimit.Policy{LowWaterMark: 50},
		Log:    slog.Default(),
	}

	// First call: returns immediately (sets internal paceUntil).
	req1, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp1, err := transport.RoundTrip(req1)
	if err != nil {
		t.Fatalf("first RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Second call: sleeps until reset + 5s before issuing the request.
	req2, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	start := time.Now()
	resp2, err := transport.RoundTrip(req2)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("second RoundTrip: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp2.StatusCode)
	}
	// Should have slept at least until reset (2s from test start). The +5s
	// buffer in production means ~7s total, but some time elapsed during the
	// first call. We check >= 2s as the minimum meaningful signal.
	if elapsed < 2*time.Second {
		t.Errorf("elapsed %v: expected >= 2s sleep for low-water-mark pacing", elapsed)
	}
}

// TestEmitsRateLimitAndRetryEvents verifies that a wait >= 1s triggers a
// RateLimit event and every retry attempt emits a Retry event with the
// attempt field set, per #82 acceptance.
func TestEmitsRateLimitAndRetryEvents(t *testing.T) {
	sink := &recordingSink{}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch hits {
		case 1:
			w.Header().Set("Retry-After", "1")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	transport := &ratelimit.Transport{
		Base:   http.DefaultTransport,
		Policy: ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second},
		Sink:   sink,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rl := sink.byKind(progress.RateLimit)
	if len(rl) != 1 {
		t.Fatalf("RateLimit events: got %d want 1", len(rl))
	}
	if got, want := rl[0].Fields["wait_duration_s"], 1; got != want {
		t.Errorf("wait_duration_s: got %v want %v", got, want)
	}
	if got := rl[0].Fields["remaining"]; got != 0 {
		t.Errorf("remaining: got %v want 0", got)
	}
	if got := rl[0].Fields["limit"]; got != 5000 {
		t.Errorf("limit: got %v want 5000", got)
	}

	retries := sink.byKind(progress.Retry)
	if len(retries) != 1 {
		t.Fatalf("Retry events: got %d want 1", len(retries))
	}
	if got := retries[0].Fields["attempt"]; got != 1 {
		t.Errorf("attempt: got %v want 1", got)
	}
	if got := retries[0].Fields["reason"]; got != "rate_limited" {
		t.Errorf("reason: got %v want rate_limited", got)
	}
}

// Sub-second waits emit a Retry event but no RateLimit event — the
// customer-visible "why is it stuck?" question only fires for waits
// long enough to register as a freeze.
func TestSubSecondWaitNoRateLimitEvent(t *testing.T) {
	sink := &recordingSink{}
	var calls int32
	fn := func() (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return mkResp(429, map[string]string{"Retry-After": "0"}), nil
		}
		return mkResp(200, nil), nil
	}
	// Use Do via RoundTrip to thread sink. Instead, use Transport with a
	// stub RoundTripper.
	transport := &ratelimit.Transport{
		Base: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return fn()
		}),
		Policy: ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second},
		Sink:   sink,
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid/", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	if rl := sink.byKind(progress.RateLimit); len(rl) != 0 {
		t.Errorf("RateLimit events: got %d want 0 (sub-second wait should not emit)", len(rl))
	}
	if retries := sink.byKind(progress.Retry); len(retries) != 1 {
		t.Errorf("Retry events: got %d want 1", len(retries))
	}
}

// Network-error retries emit a Retry event with reason=network_error.
func TestNetworkErrorEmitsRetryEvent(t *testing.T) {
	sink := &recordingSink{}
	var calls int32
	transport := &ratelimit.Transport{
		Base: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 2 {
				return nil, io.ErrUnexpectedEOF
			}
			return mkResp(200, nil), nil
		}),
		Policy: ratelimit.Policy{MaxAttempts: 3, CumulativeBudget: 5 * time.Second},
		Sink:   sink,
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid/", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	retries := sink.byKind(progress.Retry)
	if len(retries) != 1 {
		t.Fatalf("Retry events: got %d want 1", len(retries))
	}
	if got := retries[0].Fields["reason"]; got != "network_error" {
		t.Errorf("reason: got %v want network_error", got)
	}
}

// Snapshot returns the most recent X-RateLimit-* triple parsed off a
// successful response, keyed by connector.
func TestSnapshotReturnsBudget(t *testing.T) {
	resetAt := time.Now().Add(28 * time.Minute).UTC().Truncate(time.Second)
	resetUnix := strconv.FormatInt(resetAt.Unix(), 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4213")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", resetUnix)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &ratelimit.Transport{
		Base:      http.DefaultTransport,
		Policy:    ratelimit.Policy{LowWaterMark: 50},
		Connector: "github",
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	snap := transport.Snapshot()
	st, ok := snap["github"]
	if !ok {
		t.Fatalf("Snapshot missing github connector: %v", snap)
	}
	if st.Remaining != 4213 {
		t.Errorf("Remaining: got %d want 4213", st.Remaining)
	}
	if st.Limit != 5000 {
		t.Errorf("Limit: got %d want 5000", st.Limit)
	}
	if !st.ResetAt.Equal(resetAt) {
		t.Errorf("ResetAt: got %v want %v", st.ResetAt, resetAt)
	}
}

// Snapshot on a fresh Transport with no observed responses returns an
// empty map, not nil-keyed entries.
func TestSnapshotEmptyByDefault(t *testing.T) {
	transport := &ratelimit.Transport{}
	if snap := transport.Snapshot(); len(snap) != 0 {
		t.Errorf("Snapshot: got %v want empty", snap)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// TestLowWaterMarkContextCancel verifies that context cancellation during the
// pacing sleep (at the start of the next request) returns an error immediately.
func TestLowWaterMarkContextCancel(t *testing.T) {
	resetAt := time.Now().Add(60 * time.Second) // long reset window
	resetUnix := strconv.FormatInt(resetAt.Unix(), 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "5")
		w.Header().Set("X-RateLimit-Reset", resetUnix)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &ratelimit.Transport{
		Base:   http.DefaultTransport,
		Policy: ratelimit.Policy{LowWaterMark: 50},
		Log:    slog.Default(),
	}

	// First call with background context: sets paceUntil = now + ~65s.
	req1, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp1, err := transport.RoundTrip(req1)
	if err != nil {
		t.Fatalf("first RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)
	resp1.Body.Close()

	// Second call with a context cancelled after 50ms.
	// The transport sleeps waiting for paceUntil (~65s away); ctx cancels it.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	start := time.Now()
	resp2, err := transport.RoundTrip(req2)
	elapsed := time.Since(start)
	if resp2 != nil {
		_ = resp2.Body.Close()
	}

	// The request was never issued; cancellation during pacing returns an error.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got err=%v resp=%v", err, resp2)
	}
	if resp2 != nil {
		t.Errorf("expected nil response on context cancel, got %v", resp2)
	}
	// Should have returned quickly after cancel (~50ms), not waited 65s.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed %v: context cancellation took too long", elapsed)
	}
}
