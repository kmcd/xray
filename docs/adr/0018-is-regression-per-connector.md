## Status

Accepted. v0.2.x.

## Context

The `incidents.is_regression` column needed a coherent definition across
multiple error-tracking connectors. An initial implementation used a
substring match across message / title / culprit / tags for Sentry, and
`reopened_at != nil` for Bugsnag. The substring match conflated user-applied
tags (e.g. `"regression-candidate"`) with source-level state, producing false
positives.

## Decision

Sentry's `is_regression` is set from `issue.isUnhandled` only. The
substring-match path (`message` / `title` / `culprit` / tag contains
"regression") is removed. Bugsnag's `is_regression = reopened_at != nil` is
unchanged. The two sources have intentionally different definitions of
"regression"; downstream analysers consult `incidents.source` rather than
treating the column as cross-source comparable.

## Consequences

**Positive.** Eliminates false positives from user-named tags in Sentry.
The column semantics are now documented per-source in `docs/schema.md`.

**Negative.** `is_regression` is not cross-source comparable; analysers
must filter by `source` before using it.

**Neutral.** Bugsnag behaviour is unchanged.

## How to apply

`internal/connectors/sentry/issues.go` — drop the substring match in
`isRegression`. Document the per-source semantics in `docs/schema.md` and in
the `incidents` row notes of `CLAUDE.md`.
