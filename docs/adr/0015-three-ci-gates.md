## Status

Accepted. v0.1.0.

## Context

The build needed a CI gate structure that catches CVEs, security smells, and
coverage regressions pre-push. The Ruby-side `gauge_intelligence` project
(rubocop/undercover/brakeman/bundler-audit) was the reference.

## Decision

CI runs five jobs on every push: `test (ubuntu)`, `test (macos)`, `lint`
(includes gosec), `vuln` (govulncheck), `coverage` (go-test-coverage).
`bin/ship` runs the same three gates locally; `make gates` is the underlying
target.

Coverage thresholds are permissive (`0` at every level) — the report
surfaces, doesn't block. Tighten once the connector test surface stabilises
(see ADR 022).

## Consequences

**Positive.** CVEs and security smells caught pre-push. Go equivalents map
cleanly to the Ruby-side toolchain.

**Negative.** ~3 minutes per push.

**Neutral.** Coverage report surfaces without blocking; thresholds tightened
in ADR 022.

## How to apply

`.github/workflows/` — five-job CI matrix. `Makefile` — `gates` target.
`bin/ship` — thin wrapper. Run `make gates` before pushing.
