## Status

Accepted. v0.2.x.

## Context

GitHub does not expose `merge_method` to non-admin tokens. The original
heuristic used parent-count only: 2 parents → `merge`; 1 parent → `squash`.
This misclassified rebase merges as squash, because rebase also produces a
single parent. A more reliable signal was needed.

## Decision

Replace the parent-count-only heuristic with parent count plus
commit-reachability:

- 2 parents → `merge`
- 1 parent, all PR head commits reachable from merge commit → `rebase`
- 1 parent, not all reachable → `squash`

## Consequences

**Positive.** Rebase and squash are correctly distinguished.
Reachability is the signal that separates them.

**Negative.** Requires a local clone to test reachability. Adds a
`git merge-base --is-ancestor` call per merged PR.

**Neutral.** The heuristic remains a heuristic; an explicit `rolled_back`
signal from a deploy provider, when available, is authoritative.

## How to apply

`internal/connectors/github/prs.go` — `deriveMergeMethod`. Use the git clone
to test reachability via `git merge-base --is-ancestor` per PR head commit,
or read the PR's `commits` GraphQL connection and compare against
`git log --pretty=%H` from the merge commit. Add table-driven tests covering
all three branches.
