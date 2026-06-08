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

// Policy bounds retry behaviour. Defaults: 3 attempts shared, separate
// cumulative budgets per error class.
//
// CumulativeBudget covers ordinary transient errors (429 primary rate
// limit, 5xx server errors). These resolve fast — typically a few seconds
// to a minute — so a tight budget is fine.
//
// SecondaryRateLimitBudget covers GitHub's anti-burst 403s, whose
// cooldown is much longer (60s+ per retry). Keeping the budget separate
// means a single 60s secondary-RL wait doesn't eat the budget for
// subsequent transient retries, and gives realistic headroom for
// hammered-token cooldowns.
type Policy struct {
	MaxAttempts              int
	CumulativeBudget         time.Duration
	SecondaryRateLimitBudget time.Duration
	// LowWaterMark is the X-RateLimit-Remaining threshold below which the
	// transport proactively sleeps until reset + 5s to avoid mid-run stalls.
	// Zero defaults to 200.
	LowWaterMark int
}

// DefaultPolicy returns the 3-attempt policy with per-error-class budgets:
// 60s for transient errors (429 / 5xx), 600s for secondary rate limits
// (GitHub anti-burst cooldowns). The split lets a long secondary-RL wait
// run without starving the transient-error budget — and vice versa.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts:              3,
		CumulativeBudget:         60 * time.Second,
		SecondaryRateLimitBudget: 600 * time.Second,
	}
}

// secondaryRateLimitWait is the default wait applied when a response is
// recognised as GitHub's secondary (anti-burst) rate limit and no
// Retry-After header was supplied. GitHub's documentation specifically
// states "wait for at least one minute before retrying" — anything
// shorter risks immediately tripping the same limit again.
const secondaryRateLimitWait = 60 * time.Second

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
	if p.SecondaryRateLimitBudget <= 0 {
		p.SecondaryRateLimitBudget = 600 * time.Second
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 500 * time.Millisecond
	bo.MaxInterval = 10 * time.Second
	bo.MaxElapsedTime = 0 // we enforce budget ourselves
	bo.Reset()

	// Per-error-class spent counters. Secondary-RL waits don't deplete
	// the transient-error budget.
	var spent, spentSecondary time.Duration
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

		wait, isSecondaryRL := nextWait(lastResp, bo)
		var (
			thisBudget    time.Duration
			thisSpent     *time.Duration
			budgetLabel   string
		)
		if isSecondaryRL {
			thisBudget = p.SecondaryRateLimitBudget
			thisSpent = &spentSecondary
			budgetLabel = "secondary"
		} else {
			thisBudget = p.CumulativeBudget
			thisSpent = &spent
			budgetLabel = "transient"
		}
		if *thisSpent+wait > thisBudget {
			if lastResp != nil {
				return lastResp, ErrBudgetExceeded
			}
			return nil, fmt.Errorf("%w: %v", ErrBudgetExceeded, lastErr)
		}
		*thisSpent += wait

		log.Info("ratelimit: waiting before retry",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.String("budget", budgetLabel),
			slog.Duration("budget_spent", *thisSpent),
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
// the response body to look for any of GitHub's anti-burst signatures.
// Returns true on match. Patterns covered:
//
//   - "secondary rate limit" — current REST + GraphQL phrasing
//   - "abuse detection"      — older REST phrasing, still seen occasionally
//   - "exceeded a rate limit" — fallback catch-all
func isSecondaryRateLimited(resp *http.Response) bool {
	if resp.Body == nil {
		return false
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, peekLimit))
	// Re-attach for the caller. If there's more to read after peekLimit,
	// it's beyond the error envelope we care about and is lost — but the
	// retry path drains and discards bodies anyway.
	resp.Body = io.NopCloser(bytes.NewReader(buf))
	body := strings.ToLower(string(buf))
	return strings.Contains(body, "secondary rate limit") ||
		strings.Contains(body, "abuse detection") ||
		strings.Contains(body, "exceeded a rate limit")
}

// nextWait computes how long to wait before the next attempt and whether
// it is a secondary-rate-limit wait (so the caller can charge the
// appropriate budget).
//
// Preference order: Retry-After header, X-RateLimit-Reset header,
// secondary-RL signature in the body (returns secondaryRateLimitWait),
// then exponential backoff. A 403 with the secondary-RL body always
// counts as a secondary-RL retry, even when Retry-After was supplied —
// the wait amount honours the header, but the budget accounting reflects
// the actual cause.
func nextWait(resp *http.Response, bo *backoff.ExponentialBackOff) (time.Duration, bool) {
	isSecondaryRL := resp != nil &&
		resp.StatusCode == http.StatusForbidden &&
		isSecondaryRateLimited(resp)

	if resp != nil {
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			return d, isSecondaryRL
		}
		if d, ok := parseRateLimitReset(resp.Header.Get("X-RateLimit-Reset")); ok {
			return d, isSecondaryRL
		}
		if isSecondaryRL {
			return secondaryRateLimitWait, true
		}
	}
	d := bo.NextBackOff()
	if d == backoff.Stop {
		return 0, false
	}
	return d, false
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
	resp, err := Do(req.Context(), t.Policy, t.Log, func() (*http.Response, error) {
		// Per net/http contract, callers can't reuse a request body across
		// retries unless it is rewindable. RoundTripper is normally called
		// by the client which arranges GetBody; we trust that contract.
		return base.RoundTrip(req)
	})
	if err != nil || resp == nil {
		return resp, err
	}
	// Proactive primary-limit pacing: if remaining quota is below the
	// low-water mark, sleep until the reset window expires so the next
	// request starts with a full bucket instead of hitting a mid-run stall.
	lwm := t.Policy.LowWaterMark
	if lwm == 0 {
		lwm = 200
	}
	remainingStr := resp.Header.Get("X-RateLimit-Remaining")
	remaining, _ := strconv.Atoi(remainingStr)
	resetUnix, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if remainingStr != "" && resetUnix > 0 && remaining < lwm {
		resetAt := time.Unix(resetUnix, 0)
		sleep := time.Until(resetAt) + 5*time.Second
		if sleep > 0 {
			log := t.Log
			if log == nil {
				log = slog.Default()
			}
			log.Warn("ratelimit: primary limit low, sleeping until reset",
				slog.Int("remaining", remaining),
				slog.Duration("sleep", sleep),
			)
			select {
			case <-time.After(sleep):
			case <-req.Context().Done():
				// Pacing sleep is for future requests; the current response
				// was already received. Return it rather than discarding it.
				return resp, nil
			}
		}
	}
	return resp, nil
}
