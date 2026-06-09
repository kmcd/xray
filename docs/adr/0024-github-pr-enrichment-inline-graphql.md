## Status

Accepted. v0.2.x. Closes [#69](https://github.com/kmcd/xray/issues/69).

## Context

A 30-day smoke against `posthog/posthog` took ~10 hours. The customer
requirement is a 2-year window on a posthog-sized repo in 5–10 minutes
(under-1-hour ceiling). Root cause was the per-PR fan-out: `extractReviews`
(REST), `extractPRComments` (two REST loops for issue + review comments),
`extractPRReviewRequests` (a separate GraphQL call), and `fetchMergeMethod`
(two REST calls per merged PR). At 5–10k PRs over a 2-year window, that's
20–40k extra round-trips on top of the already-paginated PR list.

## Decision

PR enrichment (reviews, top-level comments, review threads, review-request
timeline events, merge-method parent count) moves off per-PR REST round-trips
and into the existing `prListQuery` GraphQL walk as inline connections.
`reviews(first: 100)`, `comments(first: 100)`,
`reviewThreads(first: 100) { comments(first: 100) }`, and
`mergeCommit { parents { totalCount } }` are read at the same time as the PR
node itself. Per-PR overflow paginators handle the long tail. Schema unchanged
— `prs`, `reviews`, `pr_comments`, `pr_review_requests` row shapes are
identical; `schema_version` stays at 2.

**A. Inline-extension over alias-batching.** Commit enrichment in `enrich.go`
uses a 25-alias-per-query batch pattern. PR enrichment is the opposite case:
every signal we need is already a connection off the PR node in the GraphQL
schema. Inline extension is strictly cheaper than alias-batching here because
it eliminates round-trips entirely rather than coalescing them.

**B. Inner-connection page size 100, with per-PR overflow.** GitHub GraphQL's
points budget is the binding constraint. 100 inner items inside 50 PRs per
page is a calculated tradeoff: enough headroom for the vast majority of PRs
without inflating per-query points for the common case. Overflow paginators
mirror the shape of the existing `paginatePRCommits` helper.

**C. Merge-method parent count from GraphQL.** ADR 021 fixed the
`merge_method` classifier to use parent count plus PR-head reachability. The
parent count is now read from `mergeCommit.parents.totalCount` in the bulk
query at zero marginal cost. The git-side `IsAncestor` reachability check
stays unchanged.

## Consequences

**Positive.** 20–40k extra round-trips eliminated for large repos. Wall-clock
on customer-scale runs reduced from hours to minutes.

**Negative.** Inner-connection page size of 100 means a PR with >100 items
fires an overflow walk. Overflow paginators are one-PR-at-a-time.

**Neutral.** Schema and row shapes unchanged; `schema_version` stays at 2.

## How to apply

`internal/connectors/github/prs.go` — extend the `prListQuery` struct with
inline `Reviews`, `Comments`, `ReviewThreads`, and `MergeCommit` connections;
teach `emitPR` to drain them inline; add overflow paginator helpers mirroring
`paginatePRCommits`. Delete the per-PR call sites (`extractReviews`,
`extractPRComments`, `extractPRReviewRequests`, `fetchMergeMethod`) once the
inline path is verified.
