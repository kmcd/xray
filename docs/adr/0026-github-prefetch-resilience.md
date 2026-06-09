## Status

Accepted. v0.3.x. Closes [#80](https://github.com/kmcd/xray/issues/80).

## Context

The very first realistic post-ADR 025 smoke (posthog 7-day, 2026-06-08) lost
the entire prefetch cache to a single mid-response EOF, dropping every PR
after the failing page. The previous behaviour — `extractPRs` emitting
whatever survived plus logging the error — kept the early pages but silently
truncated the tail. On a 2-year customer window this turns ADR 025's ~150 s
overlap claim into "150 s when the network is perfectly clean."

## Decision

Two changes work together to make the PR-list GraphQL walk resilient to
mid-response truncation:

1. `costInterceptor.RoundTrip` returns `(nil, readErr)` when
   `io.ReadAll(resp.Body)` fails mid-stream, instead of re-attaching the
   partial body and returning `(resp, nil)`. The partial body would otherwise
   surface as a downstream JSON decoder `"unexpected EOF"` that nothing
   retried.
2. `fetchPRs` wraps each `c.gql.Query(...)` call in a `queryWithEOFRetry`:
   3 attempts, 60 s cumulative budget, exponential backoff (500 ms initial,
   10 s cap), ctx-aware. Transient EOF class — `errors.Is(err, io.ErrUnexpectedEOF)`,
   `errors.Is(err, io.EOF)`, or surface-text containing `"unexpected EOF"` —
   triggers a retry against the unchanged GraphQL cursor. Non-transient errors
   return immediately.

Follow-up in the same issue extended coverage to:

- *Cursor-handoff partial cache.* `prPrefetchResult` now carries
  `nextCursor string`; `fetchPRs` accepts a `startCursor` parameter and
  returns `(nodes, resumeCursor, err)`. `extractPRs` resumes from that cursor
  when a cached Prefetch errored mid-walk.
- *EOF retry on non-Prefetch GraphQL paths.* `queryWithEOFRetry` now wraps
  every `c.gql.Query` call site in the package.

**A. Why retry in `fetchPRs`, not in `ratelimit.Transport`.** In the transport
chain, `ratelimit` sees the response before `costInterceptor` reads the body,
so a body-read failure inside the interceptor happens above the retry layer
and bypasses it entirely. Catching the surfaced error at the `gql.Query` call
site is self-contained and leaves the ratelimit retry classes untouched.

**B. Cursor stability across retries.** GitHub GraphQL cursors are opaque
continuation tokens; empirically they hold for minutes, comfortably longer
than the 60 s retry budget. The retry re-issues the same `vars["after"]`
cursor, so a successful retry resumes the same page rather than re-walking
from the start.

## Consequences

**Positive.** Mid-response EOF no longer truncates the PR list silently.
Partial caches resume from the last successful cursor rather than being
discarded.

**Negative.** `queryWithEOFRetry` is private to the github package and used
only by `fetchPRs` for now — other connectors keep their existing semantics
until a real-world failure surfaces.

**Neutral.** No new dependencies (`github.com/cenkalti/backoff/v4` already in
`go.mod` via `internal/ratelimit`).

## How to apply

`internal/connectors/github/github.go` — change `costInterceptor.RoundTrip`
to return `(nil, readErr)` on partial body read; extend `prPrefetchResult`
with `nextCursor`; update `Prefetch` / `consumePRPrefetch` to plumb it.
`internal/connectors/github/prs.go` — add `queryWithEOFRetry` +
`doJSONPOSTWithEOFRetry` + `isTransientEOF`; `fetchPRs(ctx, repo, window,
startCursor)` returns resumeCursor; `extractPRs` resumes from cursor when
Prefetch errored.
`internal/connectors/github/{reviews,pr_comments,pr_meta,branches,enrich}.go`
— swap to the retry helpers.
Tests: `internal/connectors/github/prs_eof_retry_test.go`.
