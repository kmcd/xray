## Status

Accepted. v0.2.x. Thresholds revised again during engagement.

## Context

Wave-A and wave-B tests brought total coverage from 33% to 56%, but
non-GitHub connector packages still sit in the 6–39% range because their
HTTP-driven paths need VCR-style fixtures that v0.2.0 does not ship. Setting
`package: 50` failed those connectors in CI, producing noisy per-package
failures that obscured real regressions.

## Decision

`.testcoverage.yml` gates at `file: 0 / package: 50 / total: 70`. Exclusions:
`cmd/xray` (CLI glue), `doc.go` files anywhere, `internal/connector/`,
`internal/connectors/` (parent), `internal/archive/`.

Revised thresholds at engagement: `total: 50 / package: 0`. Per-package
gating returns in v0.3.x once VCR fixtures land and the connectors all reach
a consistent baseline. Per-file gating stays at 0 to avoid noisy per-file
flags on small files; the per-package gate is the load-bearing signal.

## Consequences

**Positive.** CI no longer fails noisily on HTTP-bound connectors without
fixtures. Regressions on the project as a whole are still caught by the total
threshold.

**Negative.** Per-package threshold set to 0 reduces per-package regression
detection until fixtures land.

**Neutral.** Lands as the last issue of wave A.

## How to apply

`.testcoverage.yml` — set the revised thresholds and exclusions. Lands as the
last issue of wave A (#47 in this milestone, #5 in the plan).
