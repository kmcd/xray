package ratelimit_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
