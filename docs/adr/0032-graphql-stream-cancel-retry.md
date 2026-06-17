# ADR 0032: GraphQL HTTP/2 stream CANCEL slow-path retry

**Status:** Accepted
**Date:** 2026-06-17
**Issue:** #176

## Context

GitHub's GraphQL API returns HTTP/2 stream CANCEL errors (`; cancel; received from peer`) on long extraction windows — typically after the primary-cap wait described in #173. The errors are silent: the client gets no data, the walk truncates silently, and `PaginationComplete` would stay `true` despite partial results.

`queryWithEOFRetry` already handled fast transient errors (EOF, connection reset). CANCEL is structurally different: GitHub imposes the reset during or after a primary-rate-limit hold, so the retry must wait long enough for the rate-limit window to clear. A fast-path retry (500ms–10s) would hit the same CANCEL immediately.

## Decision

Extend `queryWithEOFRetry` with a second independent backoff policy for CANCEL:

- Initial interval: 30 s (matches GitHub's primary-cap hold window)
- Max interval: 5 min
- Max attempts: 6
- Max elapsed: 16 min (6 × ~2.5 min average across exponential steps)

EOF/connection-reset retain their existing fast policy (500ms→10s, 3 attempts, 60s budget). The two policies are independent: a page that gets both an EOF and a CANCEL counts against separate budgets.

A connector-level `atomic.Int64` (`streamCancelRetries`) tracks total retries. Each `Extract` call snapshots the counter at start and end; the delta lands in `Provenance.StreamCancelRetries`, which the manifest and summary surface.

`hasErrors()` in `run.go` already checked `len(p.Errors) > 0`. Extend it to also return `true` when `!p.PaginationComplete` so a walk truncated by exhausted CANCEL retries exits with code 2 rather than 0.

## Alternatives considered

**Increase EOF retry budget to cover CANCEL.** Rejected: EOF retries should be fast (network hiccup); conflating them with CANCEL would slow down the common EOF case.

**New query method / separate retry wrapper.** Rejected: `queryWithEOFRetry` is already the single retry boundary for all GQL calls; duplicating it would split retry logic and risk divergence.

**Propagate `*Provenance` through `queryWithEOFRetry` to count retries.** Attempted. Requires updating 10+ call sites and is invasive. `atomic.Int64` with a per-extraction snapshot achieves the same result with zero call-site changes.

## Consequences

- Long windows that previously returned exit 0 with silently truncated PR/review sets now return exit 2 with `stream_cancel_retries` recorded in the manifest.
- Total retry wait on a worst-case CANCEL sequence (6 attempts, max intervals) is ~16 min. This is intentional: the alternative is a permanently incomplete artifact with no indication something went wrong.
- `Provenance.StreamCancelRetries` is a non-breaking addition (new field, omitted when zero); no `schema_version` bump required.
