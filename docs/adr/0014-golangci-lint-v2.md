## Status

Accepted. v0.1.0.

## Context

golangci-lint v1 refuses to lint a Go 1.26 target. The v2 schema is
incompatible with v1, requiring a one-shot config rewrite.

## Decision

golangci-lint v2.12.2. Config rewritten in v2 schema. `gosec` enabled inline.

Lint tuning:
- `revive`'s `exported` rule disabled — every `internal/` symbol would demand
  a docstring; not worth it pre-1.0.
- `errcheck.exclude-functions` covers idiomatic `fmt.Fprint*` to stdio and
  `defer x.Close()`.
- `gosec` excludes `G104` (better covered by errcheck) and `G122` (TOCTOU
  symlink races on our own per-run temp dirs — not a threat model we care
  about).
- Remaining `gosec` flagged sites carry `#nosec` annotations with one-line
  justifications.

## Consequences

**Positive.** Signal-to-noise kept high. Security-relevant checks remain
active. Compatible with Go 1.26.4.

**Negative.** v1 → v2 migration was a one-shot rewrite of `.golangci.yml`.

**Neutral.** Same Go-toolchain constraint as ADR 013.

## How to apply

`.golangci.yml` — v2 schema. `.github/workflows/` — golangci-lint action
pinned to the v2 binary. `make lint` runs locally.
