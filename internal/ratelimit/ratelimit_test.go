package ratelimit_test

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/ratelimit"
)

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
