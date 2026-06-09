## Status

Accepted. v0.1.0.

## Context

`govulncheck` against the v0.1.0 connector and ratelimit code surfaced six
stdlib CVEs whose fixes only ship in Go 1.26.4: `net/textproto` GO-2026-5039,
`crypto/x509` GO-2026-5037, plus 4 others reachable from the ratelimit
transport. The original `go.mod` directive was `go 1.23`.

## Decision

`go.mod` directive is `go 1.26.4`. CI's `setup-go` uses `go-version-file`,
which pins the toolchain version to match. `go.sum` is committed as part of
this change — resolving the deferred action from ADR 002.

## Consequences

**Positive.** All six CVEs addressed. `go.sum` committed; builds are
reproducible.

**Negative.** Anyone building from source needs Go 1.26.4+. Pre-1.0,
acceptable.

**Neutral.** ADR 002's deferred `go.sum` commit resolved as part of this
change.

## How to apply

`go.mod` — `go 1.26.4` directive. `.github/workflows/` — `setup-go` with
`go-version-file`. Run `govulncheck ./...` to verify no remaining CVEs.
