## Status

Accepted. v0.3.x. Closes [#71](https://github.com/kmcd/xray/issues/71).

## Context

Post-ADR 024, the github connector's wall-clock on `posthog/posthog` 7-day
is dominated by the PR GraphQL walk (~9 min), which is API-bound and only
depends on the local clone for `resolveMergeMethod` (a per-PR git
`merge-base --is-ancestor` check, fast). The clone itself takes ~68 s and
the clone-bound `extractCommits` walk takes ~70 s — both could overlap with
the PR fetch with no semantic change. Today they are serial; prefetching
recovers that wall-clock without changing the connector contract or any row
shape. Estimated savings on the 7-day smoke: ~150 s (22% reduction).

## Decision

Add an optional `connector.Prefetcher` interface to the connector contract:

```go
type Prefetcher interface {
    Prefetch(ctx context.Context, slug string, window Window) error
}
```

Connectors that implement it expose a per-repo entry point that `xray run`'s
clone phase invokes as a goroutine alongside `git.Clone(...)`. The result is
stashed on the connector (per-slug) and consumed by `Extract` later.
`Extract` remains the canonical row-emit path; `Prefetch` is purely a
wall-clock hint. Non-implementing connectors are skipped at the run.go-side
type assertion so the change is zero-impact on sentry / circleci / bugsnag /
honeycomb / githubactions.

**A. Opt-in extension, not a required signature.** The connector contract
from ADR 022 stays unchanged. `Prefetcher` is a hint — connectors implement
it only when they have meaningful API-bound work that doesn't need the clone.

**B. Per-slug cache on the connector, not on the `connector.Repo` struct.**
Stashing prefetch state on the connector keeps the canonical `Repo` and
`Provenance` shapes unchanged. `consumePRPrefetch` removes the entry on read,
so a subsequent `Extract` for the same slug falls back to a live fetch.

**C. Two-fragment Provenance merge inside `Extract`.** The github `Extract`
runs clone-bound stages in goroutine A and the PR stage in goroutine B. Each
writes to its own `connector.Provenance` fragment; the two are folded into
the returned `prov` via a new `(*Provenance).Merge` helper. Policy:
`RowsReturned` summed, `Errors` first-wins per context, `PaginationComplete`
ANDed, `RateLimitTruncated` ORed, `Endpoints` and `Flags` first-wins.

## Consequences

**Positive.** ~150 s recovered on 7-day smokes; larger windows recover more
clone time. Zero impact on non-GitHub connectors.

**Negative.** Prefetch adds complexity to the GitHub connector; the two
goroutines require a `Provenance.Merge` helper and careful per-slug cache
management.

**Neutral.** The interface is in place for non-GitHub connectors to opt in
if a slow API stage warrants it in the future.

## How to apply

`internal/connector/connector.go` — add the `Prefetcher` interface and
`(*Provenance).Merge`. `internal/connectors/github/github.go` — add
`prefetchMu`, `prefetchData`, `Prefetch`, `consumePRPrefetch`.
`internal/connectors/github/prs.go` — split the existing `extractPRs` into
`fetchPRs` + `emitPRs` + a thin cache-aware `extractPRs`.
`internal/connectors/github/extract.go` — split per-repo `Extract` into sync
prelude, parallel block (two goroutines), and sync postlude.
`internal/run/run.go` — fire `conn.Prefetch(ctx, slug, win)` as a goroutine
alongside `git.Clone(...)`.
