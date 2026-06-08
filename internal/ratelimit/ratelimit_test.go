package ratelimit_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/ratelimit"
)

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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 403 {
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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 400 {
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
	_, err := ratelimit.Do(ctx, p, nil, fn)
	if err == nil {
		t.Fatalf("expected error on cancellation")
	}
	if err != context.Canceled {
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
	if resp.StatusCode != 200 {
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
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
}

// TestLowWaterMarkSleep verifies that Transport sleeps until the reset window
// when X-RateLimit-Remaining falls below LowWaterMark.
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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	start := time.Now()
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	// Should have slept at least until reset (2s) plus 5s buffer minus a
	// small scheduling tolerance. We check >= 2s (the reset delta) as the
	// minimum meaningful signal; the +5s buffer means it will be ~7s total
	// in production but we don't want slow tests.
	if elapsed < 2*time.Second {
		t.Errorf("elapsed %v: expected >= 2s sleep for low-water-mark pacing", elapsed)
	}
}

// TestLowWaterMarkContextCancel verifies that context cancellation during the
// low-water-mark sleep returns ctx.Err() immediately.
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

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	start := time.Now()
	resp, err := transport.RoundTrip(req)
	elapsed := time.Since(start)

	// Context cancel during the LWM sleep should return the already-received
	// valid response (not nil), since the current request succeeded; the sleep
	// was only to pace future requests.
	if err != nil {
		t.Fatalf("RoundTrip: expected nil error on cancel-during-sleep, got %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Errorf("expected valid 200 response, got %v", resp)
	}
	// Should have returned quickly after cancel (~50ms), not waited 60s.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed %v: context cancellation took too long", elapsed)
	}
}
