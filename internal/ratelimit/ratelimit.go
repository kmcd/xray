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
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/kmcd/xray/internal/progress"
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

// Adaptive pacing ladder applied after a secondary-RL trigger. The
// reactive Retry-After wait alone does not break the storm: GitHub's
// per-token secondary limit re-trips within a few hundred ms of
// resuming, so every subsequent request needs a small inter-call sleep
// until the budget recovers. Issue #150 evidence: ~600ms between
// consecutive 60s waits on a single-PAT long-window run.
//
// Ladder: 500ms -> 1s -> 2s -> 4s -> 5s (capped). 500ms is just above
// the observed re-trip interval; one escalation to 1s clears it
// decisively. 5s cap keeps tail latency tolerable vs the 60s reactive
// waits the storm would otherwise incur.
const (
	adaptivePaceInitial    = 500 * time.Millisecond
	adaptivePaceMultiplier = 2
	adaptivePaceMax        = 5 * time.Second
	adaptivePaceDecay      = 30 * time.Minute
)

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
	return doWithHooks(ctx, p, log, hooks{}, fn)
}

// hooks carries optional observation callbacks. A zero hooks is the
// no-emit path used by Do.
type hooks struct {
	sink      progress.Sink
	connector string
	now       func() time.Time
	// onSecondaryRL fires once per secondary-RL detection (the same
	// detection that routes the wait to SecondaryRateLimitBudget). The
	// Transport supplies escalateAdaptivePace here so its pacing ladder
	// advances on every confirmed hit; Do's zero-hooks path leaves it
	// nil and the trigger is a no-op.
	onSecondaryRL func()
}

func (h hooks) emit(ev progress.Event) {
	if h.sink == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = h.timeNow()
	}
	h.sink.Emit(ev)
}

func (h hooks) timeNow() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// rateLimitEventThreshold is the minimum wait that triggers a
// RateLimit progress event. Sub-second backoff is noise — the
// customer-visible "why is it stuck?" question only fires for waits
// long enough to register as a freeze. Per #82 acceptance.
const rateLimitEventThreshold = time.Second

func doWithHooks(ctx context.Context, p Policy, log *slog.Logger, h hooks, fn func() (*http.Response, error)) (*http.Response, error) {
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
			thisBudget  time.Duration
			thisSpent   *time.Duration
			budgetLabel string
		)
		if isSecondaryRL {
			if h.onSecondaryRL != nil {
				h.onSecondaryRL()
			}
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
			// %v (not %w) for lastErr is deliberate: wrapping a
			// context.Canceled lastErr would make errors.Is(err,
			// context.Canceled) at cmd/xray/run.go:142 misroute a
			// budget-exhaustion as a graceful interrupt (exit 130).
			// Budget exhaustion is its own failure class; the diagnostic
			// text is the only thing the caller needs from lastErr.
			//nolint:errorlint // see comment above
			return nil, fmt.Errorf("%w: %v", ErrBudgetExceeded, lastErr)
		}
		*thisSpent += wait

		log.Info("ratelimit: waiting before retry",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.String("budget", budgetLabel),
			slog.Duration("budget_spent", *thisSpent),
		)

		emitWaitAndRetry(h, lastResp, lastErr, attempt, wait, isSecondaryRL)

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

// emitWaitAndRetry emits a RateLimit event for any wait > 1s and a
// Retry event for every retry attempt (per #82 acceptance criteria).
func emitWaitAndRetry(h hooks, resp *http.Response, err error, attempt int, wait time.Duration, isSecondaryRL bool) {
	if h.sink == nil {
		return
	}
	waitSecs := int(wait / time.Second)
	if wait >= rateLimitEventThreshold {
		fields := map[string]any{
			"wait_duration_s": waitSecs,
			"secondary":       isSecondaryRL,
		}
		if resp != nil {
			if v, ok := atoiHeader(resp.Header.Get("X-RateLimit-Remaining")); ok {
				fields["remaining"] = v
			}
			if v, ok := atoiHeader(resp.Header.Get("X-RateLimit-Limit")); ok {
				fields["limit"] = v
			}
			if v, ok := unixHeader(resp.Header.Get("X-RateLimit-Reset")); ok {
				fields["reset_at"] = v
			}
		}
		h.emit(progress.Event{
			Kind:      progress.RateLimit,
			Connector: h.connector,
			Message:   fmt.Sprintf("rate limited, waiting %ds", waitSecs),
			Fields:    fields,
		})
	}
	reason := "transient"
	switch {
	case err != nil:
		reason = "network_error"
	case isSecondaryRL:
		reason = "secondary_rate_limit"
	case resp != nil && resp.StatusCode == http.StatusTooManyRequests:
		reason = "rate_limited"
	case resp != nil && resp.StatusCode >= 500:
		reason = "server_error"
	}
	h.emit(progress.Event{
		Kind:      progress.Retry,
		Connector: h.connector,
		Message:   fmt.Sprintf("retry attempt %d", attempt),
		Fields: map[string]any{
			"attempt":         attempt,
			"reason":          reason,
			"wait_duration_s": waitSecs,
		},
	})
}

func atoiHeader(v string) (int, bool) {
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func unixHeader(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
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
	// Sink receives progress.RateLimit and progress.Retry events on
	// wait/retry. Nil sink (zero value) silently no-ops; existing slog
	// output is unchanged either way. Spec docs/spec.md:464-469.
	Sink progress.Sink
	// Connector overrides the host→connector mapping used to label
	// emitted events and budget snapshots. Empty falls back to
	// hostToConnector(req.URL.Host).
	Connector string
	// paceUntil stores a unix-nanosecond timestamp set when a response
	// indicates remaining quota is below LowWaterMark. The next RoundTrip
	// call sleeps until this time, so the goroutine that received the
	// triggering response is never blocked by the pacing sleep.
	paceUntil  atomic.Int64
	paceReason atomic.Value // string: "primary_low_water" or "graphql_low_water"
	// adaptivePaceNanos is the current inter-request delay applied
	// before every RoundTrip after a secondary-RL hit. Escalated by
	// escalateAdaptivePace; decayed linearly on read in
	// currentAdaptivePace. Zero means no adaptive pacing in effect.
	// Issue #150: reactive Retry-After waits alone do not break the
	// per-token secondary-RL storm — every subsequent request needs a
	// small inter-call sleep until the budget recovers.
	adaptivePaceNanos atomic.Int64
	// adaptiveTriggerNanos is the unix-nanosecond wall-clock of the
	// most recent secondary-RL trigger. Drives the linear decay back
	// to zero over adaptivePaceDecay since the last hit.
	adaptiveTriggerNanos atomic.Int64
	budgets   budgetTracker
	// warned tracks per-connector predictive-exhaustion warnings so we
	// emit at most one PhaseError event per connector per Transport
	// lifetime (acceptance: "a single ... warning event").
	warned sync.Map
	// startedAt is the first RoundTrip's wall-clock moment, used to gate
	// the "5+ minutes left" leg of the predictive heuristic in the
	// absence of an ETA from #81.
	startedAt atomic.Int64
}

// Snapshot returns the current rate-limit budget per connector. Empty
// map if no rate-limit headers have been observed.
func (t *Transport) Snapshot() map[string]BudgetState {
	return t.budgets.snapshot()
}

// gqlBudgetLimit is GitHub's fixed GraphQL cost-unit budget per hour.
const gqlBudgetLimit = 5000

// SetGQLPacing marks the transport to sleep before the next request until
// the supplied time, attributing the wait to the GraphQL low-water-mark.
// Called by costInterceptor when throttleStatus.remaining falls below the
// GraphQL low-water mark. The pacing sleep fires at the start of the next
// RoundTrip call, matching the REST low-water path.
func (t *Transport) SetGQLPacing(until time.Time) {
	t.paceReason.Store("graphql_low_water")
	t.paceUntil.Store(until.UnixNano())
}

// UpdateGQLBudget records the current GraphQL cost-unit budget under the
// "github-graphql" connector key in the budget tracker, enabling Snapshot
// and maybeEmitPredictiveWarning to operate on GQL quota independently of
// the REST X-RateLimit-Remaining budget.
func (t *Transport) UpdateGQLBudget(remaining int, resetAt time.Time, sink progress.Sink) {
	st := BudgetState{
		Remaining:    remaining,
		HasRemaining: true,
		Limit:        gqlBudgetLimit,
		ResetAt:      resetAt.UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	t.budgets.mu.Lock()
	if t.budgets.m == nil {
		t.budgets.m = make(map[string]BudgetState)
	}
	t.budgets.m["github-graphql"] = st
	t.budgets.mu.Unlock()
	t.maybeEmitPredictiveWarning(sink, "github-graphql")
}

// loadPaceReason returns the stored pacing reason or "primary_low_water" if
// none has been set yet.
func (t *Transport) loadPaceReason() string {
	if v, ok := t.paceReason.Load().(string); ok && v != "" {
		return v
	}
	return "primary_low_water"
}

// escalateAdaptivePace bumps the adaptive inter-request pace one step
// up the ladder (adaptivePaceInitial → ×adaptivePaceMultiplier each
// step, capped at adaptivePaceMax) and resets the decay clock. Called
// from doWithHooks via the onSecondaryRL hook on every secondary-RL
// detection. CAS loop guarantees parallel hits both escalate rather
// than stomping each other to the initial value.
func (t *Transport) escalateAdaptivePace() {
	now := time.Now().UnixNano()
	for {
		cur := t.adaptivePaceNanos.Load()
		var next int64
		switch {
		case cur <= 0:
			next = adaptivePaceInitial.Nanoseconds()
		case cur >= adaptivePaceMax.Nanoseconds():
			next = adaptivePaceMax.Nanoseconds()
		default:
			next = cur * int64(adaptivePaceMultiplier)
			if next > adaptivePaceMax.Nanoseconds() {
				next = adaptivePaceMax.Nanoseconds()
			}
		}
		if t.adaptivePaceNanos.CompareAndSwap(cur, next) {
			t.adaptiveTriggerNanos.Store(now)
			return
		}
	}
}

// currentAdaptivePace returns the in-effect inter-request delay,
// linearly decayed toward zero over adaptivePaceDecay since the last
// trigger. Returns 0 once the decay window has elapsed. Cheap atomic
// loads on the hot path; no goroutine.
func (t *Transport) currentAdaptivePace(now time.Time) time.Duration {
	base := t.adaptivePaceNanos.Load()
	if base <= 0 {
		return 0
	}
	trigger := t.adaptiveTriggerNanos.Load()
	if trigger <= 0 {
		return time.Duration(base)
	}
	elapsed := now.UnixNano() - trigger
	decayNs := adaptivePaceDecay.Nanoseconds()
	if elapsed >= decayNs {
		// Decay complete. Deliberately do NOT zero out
		// adaptivePaceNanos here: a concurrent escalateAdaptivePace at
		// the ladder cap performs a CAS(cap, cap) no-op then stores a
		// fresh trigger. Between our two atomic loads above, an
		// escalator could refresh the trigger while leaving the base
		// pace value unchanged; our subsequent CAS(base, 0) would
		// still succeed (cur matches) and silently drop the
		// escalation. Returning 0 without zeroing leaves the state
		// for the next reader, which will re-read the fresh trigger
		// and apply the correct decayed value. The cost is a few
		// extra atomic loads and one multiplication per RoundTrip
		// long after the storm has passed — negligible vs the per-
		// request network latency.
		return 0
	}
	if elapsed <= 0 {
		return time.Duration(base)
	}
	// Compute base * (decayNs - elapsed) / decayNs without int64
	// overflow. With base ≤ adaptivePaceMax (5s = 5e9 ns) and decay =
	// 30 min (1.8e12 ns), base*elapsed reaches ~9e21 — overflows int64
	// (max ~9.2e18). Scale to milliseconds: base*elapsedMs ≤ 5e9 *
	// 1.8e6 = 9e15, safely within range. Precision loss is sub-ms,
	// well below any caller-visible tolerance.
	const msNs = int64(time.Millisecond)
	elapsedMs := elapsed / msNs
	decayMs := decayNs / msNs
	remaining := base - (base*elapsedMs)/decayMs
	if remaining < 0 {
		return 0
	}
	return time.Duration(remaining)
}

// effectiveSink returns t.Sink if set, otherwise the ambient Sink on
// the request context. Connectors are constructed before the run-wide
// Sink exists, so the field is rarely populated; the CLI installs the
// real sink on the run context via progress.WithSink.
func (t *Transport) effectiveSink(ctx context.Context) progress.Sink {
	if t.Sink != nil {
		return t.Sink
	}
	return progress.FromContext(ctx)
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	connector := t.Connector
	if connector == "" {
		connector = hostToConnector(req.URL.Host)
	}

	sink := t.effectiveSink(req.Context())

	t.startedAt.CompareAndSwap(0, time.Now().UnixNano())

	// Proactive primary-limit pacing: if a prior response set paceUntil,
	// sleep before issuing this request. Sleeping here (not after the prior
	// response) means the goroutine that received the triggering response is
	// never blocked by the pacing sleep.
	if until := t.paceUntil.Load(); until > 0 {
		now := time.Now().UnixNano()
		if sleep := time.Duration(until - now); sleep > 0 {
			log := t.Log
			if log == nil {
				log = slog.Default()
			}
			log.Warn("ratelimit: primary limit low, sleeping until reset",
				slog.Duration("sleep", sleep),
			)
			if sleep >= rateLimitEventThreshold {
				sink.Emit(progress.Event{
					Kind:      progress.RateLimit,
					Connector: connector,
					Message:   fmt.Sprintf("primary limit low, waiting %ds", int(sleep/time.Second)),
					At:        time.Now(),
					Fields: map[string]any{
						"wait_duration_s": int(sleep / time.Second),
						"reason":          t.loadPaceReason(),
					},
				})
			}
			timer := time.NewTimer(sleep)
			select {
			case <-timer.C:
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			}
		}
	}

	// Adaptive secondary-RL pacing: if a prior response triggered
	// secondary-RL detection, sleep currentAdaptivePace before issuing
	// this request to stay below the per-token burst threshold.
	// Composes additively with the paceUntil block above — the two have
	// orthogonal triggers (low-water-remaining vs anti-burst hit) and
	// can both apply simultaneously.
	if pace := t.currentAdaptivePace(time.Now()); pace > 0 {
		if pace >= rateLimitEventThreshold {
			// Round to the nearest integer-second for the event. The
			// linear decay continuously shaves a hair off the ladder
			// value so truncation would report N-1 for a step the user
			// configured at N (e.g. 1.9998s → "1s"). Round preserves
			// the ladder semantics in the customer-visible field.
			waitS := int((pace + time.Second/2) / time.Second)
			sink.Emit(progress.Event{
				Kind:      progress.RateLimit,
				Connector: connector,
				Message:   fmt.Sprintf("adaptive pacing, waiting %ds", waitS),
				At:        time.Now(),
				Fields: map[string]any{
					"wait_duration_s": waitS,
					"reason":          "secondary_adaptive",
				},
			})
		}
		timer := time.NewTimer(pace)
		select {
		case <-timer.C:
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		}
	}

	resp, err := doWithHooks(req.Context(), t.Policy, t.Log, hooks{sink: sink, connector: connector, onSecondaryRL: t.escalateAdaptivePace}, func() (*http.Response, error) {
		// Per net/http contract, callers can't reuse a request body across
		// retries unless it is rewindable. RoundTripper is normally called
		// by the client which arranges GetBody; we trust that contract.
		return base.RoundTrip(req)
	})
	if err != nil || resp == nil {
		return resp, err
	}

	t.budgets.update(connector, resp.Header, time.Now().UTC())
	t.maybeEmitPredictiveWarning(sink, connector)

	// After a successful response: if remaining quota is below the low-water
	// mark, schedule a sleep before the next request by setting paceUntil.
	// This never blocks the current goroutine.
	lwm := t.Policy.LowWaterMark
	if lwm == 0 {
		lwm = 200
	}
	remainingStr := resp.Header.Get("X-RateLimit-Remaining")
	remaining, remErr := strconv.Atoi(remainingStr)
	if remainingStr != "" && remErr == nil && remaining < lwm {
		if d, ok := parseRateLimitReset(resp.Header.Get("X-RateLimit-Reset")); ok && d > 0 {
			t.paceReason.Store("primary_low_water")
			t.paceUntil.Store(time.Now().Add(d + 5*time.Second).UnixNano())
		}
	}

	return resp, nil
}

// Thresholds for the predictive-exhaustion warning. We warn when
// Remaining drops below warnBelow with 5+ minutes of run elapsed, and
// clear the per-connector latch once Remaining recovers above
// clearAbove — so a transient quota dip that recovers does not leave a
// stale "throttling imminent" warning visible for the rest of the run.
const (
	predictWarnBelow  = 100
	predictClearAbove = 200
)

// maybeEmitPredictiveWarning fires a PhaseError event when remaining
// budget drops below predictWarnBelow with at least 5 minutes of run
// elapsed, at most once per connector per dip — clears the latch when
// the budget recovers above predictClearAbove. Skips entirely when
// X-RateLimit-Remaining was not present in the most recent response
// (HasRemaining false), avoiding the Remaining=0 false positive when
// only Limit/Reset headers came back.
func (t *Transport) maybeEmitPredictiveWarning(sink progress.Sink, connector string) {
	if sink == nil {
		return
	}
	st, ok := t.budgets.get(connector)
	if !ok || !st.HasRemaining {
		return
	}
	if st.Remaining >= predictClearAbove {
		t.warned.Delete(connector)
		return
	}
	if st.Remaining >= predictWarnBelow {
		return
	}
	started := t.startedAt.Load()
	if started == 0 || time.Since(time.Unix(0, started)) < 5*time.Minute {
		return
	}
	if _, loaded := t.warned.LoadOrStore(connector, struct{}{}); loaded {
		return
	}
	sink.Emit(progress.Event{
		Kind:      progress.PhaseError,
		Connector: connector,
		Message:   fmt.Sprintf("rate-limit budget low: %d remaining; throttling imminent", st.Remaining),
		At:        time.Now(),
		Fields: map[string]any{
			"remaining": st.Remaining,
			"limit":     st.Limit,
			"reset_at":  st.ResetAt,
		},
	})
}
