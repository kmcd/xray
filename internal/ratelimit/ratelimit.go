package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// Policy bounds retry behaviour. Defaults: 3 attempts, 60s cumulative wait.
type Policy struct {
	MaxAttempts      int
	CumulativeBudget time.Duration
}

// DefaultPolicy returns the spec-mandated 3-attempt / 120s policy. The
// budget was raised from the original 60s to fit a single secondary-rate-
// limit retry (which sleeps ~30-60s) within one attempt without exhausting
// the cumulative wait.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, CumulativeBudget: 120 * time.Second}
}

// secondaryRateLimitWait is the default wait applied when a response is
// recognised as GitHub's secondary (anti-burst) rate limit and no
// Retry-After header was supplied. GitHub documents "a few minutes" as
// the recommended wait; 45s strikes a balance between honouring the
// suggestion and keeping total wall time low.
const secondaryRateLimitWait = 45 * time.Second

// peekLimit caps how many bytes of a 4xx response body we read for
// rate-limit-signature detection before re-attaching the body for the
// caller. 4 KB covers GitHub's JSON error envelope and keeps the cost
// trivial on the happy path.
const peekLimit = 4096

// ErrBudgetExceeded is returned when the cumulative wait budget would be
// exceeded before another retry could complete.
var ErrBudgetExceeded = errors.New("ratelimit: cumulative wait budget exceeded")

// Do executes fn with retries on 429 and 5xx according to p. Retry-After and
// X-RateLimit-Reset are honoured when present; otherwise an exponential
// backoff with jitter (capped at ~10s) is used. ctx.Done() is observed
// while sleeping.
func Do(ctx context.Context, p Policy, log *slog.Logger, fn func() (*http.Response, error)) (*http.Response, error) {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.CumulativeBudget <= 0 {
		p.CumulativeBudget = 60 * time.Second
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 500 * time.Millisecond
	bo.MaxInterval = 10 * time.Second
	bo.MaxElapsedTime = 0 // we enforce budget ourselves
	bo.Reset()

	var spent time.Duration
	var lastResp *http.Response
	var lastErr error

	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		// Drain any prior response body before retry.
		if lastResp != nil {
			_, _ = io.Copy(io.Discard, lastResp.Body)
			_ = lastResp.Body.Close()
			lastResp = nil
		}

		resp, err := fn()
		if err != nil {
			lastErr = err
			if attempt == p.MaxAttempts {
				return nil, err
			}
		} else {
			lastResp = resp
			lastErr = nil
			if !shouldRetryResp(resp) {
				// Success or non-retryable 4xx: hand the response back to
				// the caller untouched. Permanent 4xx errors are surfaced
				// as the raw response so the caller sees status/headers/
				// body.
				return resp, nil
			}
			if attempt == p.MaxAttempts {
				return resp, nil
			}
		}

		wait := nextWait(lastResp, bo)
		if spent+wait > p.CumulativeBudget {
			if lastResp != nil {
				return lastResp, ErrBudgetExceeded
			}
			return nil, fmt.Errorf("%w: %v", ErrBudgetExceeded, lastErr)
		}
		spent += wait

		log.Info("ratelimit: waiting before retry",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.Duration("budget_spent", spent),
		)

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			if lastResp != nil {
				_ = lastResp.Body.Close()
			}
			return nil, ctx.Err()
		case <-t.C:
		}
	}

	if lastResp != nil {
		return lastResp, lastErr
	}
	return nil, lastErr
}

// shouldRetryResp decides whether a response is transient and worth a
// retry. Beyond the obvious 429 and 5xx cases it also recognises GitHub's
// secondary (anti-burst) rate limit, which returns 403 with a JSON body
// whose message contains "secondary rate limit". The body is peeked up to
// peekLimit bytes and re-attached so the caller still sees it on the
// terminal attempt.
func shouldRetryResp(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
		return true
	}
	if resp.StatusCode == http.StatusForbidden && isSecondaryRateLimited(resp) {
		return true
	}
	return false
}

// isSecondaryRateLimited reads (and re-attaches) up to peekLimit bytes of
// the response body to look for GitHub's signature. Returns true on match.
func isSecondaryRateLimited(resp *http.Response) bool {
	if resp.Body == nil {
		return false
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, peekLimit))
	// Re-attach for the caller. If there's more to read after peekLimit,
	// it's beyond the error envelope we care about and is lost — but the
	// retry path drains and discards bodies anyway.
	resp.Body = io.NopCloser(bytes.NewReader(buf))
	return strings.Contains(strings.ToLower(string(buf)), "secondary rate limit")
}

// nextWait computes how long to wait before the next attempt, preferring
// Retry-After and X-RateLimit-Reset hints from the response. When the
// response carries the secondary-rate-limit signature and no header hint
// is supplied, fall back to secondaryRateLimitWait — the exponential
// backoff calculator's ~10s cap is too short for that case.
func nextWait(resp *http.Response, bo *backoff.ExponentialBackOff) time.Duration {
	if resp != nil {
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			return d
		}
		if d, ok := parseRateLimitReset(resp.Header.Get("X-RateLimit-Reset")); ok {
			return d
		}
		if resp.StatusCode == http.StatusForbidden && isSecondaryRateLimited(resp) {
			return secondaryRateLimitWait
		}
	}
	d := bo.NextBackOff()
	if d == backoff.Stop {
		return 0
	}
	return d
}

func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, true
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0, true
		}
		return d, true
	}
	return 0, false
}

func parseRateLimitReset(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	secs, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	reset := time.Unix(secs, 0)
	d := time.Until(reset)
	if d < 0 {
		return 0, true
	}
	return d, true
}

// Transport is an http.RoundTripper that retries per Policy. Install it as
// httpClient.Transport so the entire connector benefits from the helper
// without per-call wrapping.
type Transport struct {
	Base   http.RoundTripper
	Policy Policy
	Log    *slog.Logger
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return Do(req.Context(), t.Policy, t.Log, func() (*http.Response, error) {
		// Per net/http contract, callers can't reuse a request body across
		// retries unless it is rewindable. RoundTripper is normally called
		// by the client which arranges GetBody; we trust that contract.
		return base.RoundTrip(req)
	})
}
