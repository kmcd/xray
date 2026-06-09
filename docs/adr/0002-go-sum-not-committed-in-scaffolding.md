## Status

Accepted. v0.1.0. Superseded: `go.sum` committed as part of ADR 013.

## Context

During the initial v0.1.0 scaffolding, there was no Go toolchain on the dev
machine, so `go.sum` could not be generated locally. Parallel agent work
needed to proceed without round-tripping through CI.

## Decision

`go.mod` ships with all expected dependencies; `go.sum` is not generated
locally and is not committed. The first CI run materialises `go.sum` for the
runner via `go mod download`.

## Consequences

**Positive.** Unblocks parallel agent work without waiting for CI.

**Negative.** Non-reproducible builds until `go.sum` is committed.

**Neutral.** Resolved as part of ADR 013 — `go.sum` now ships with the repo.

## How to apply

N/A — superseded by ADR 013.
