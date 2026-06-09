## Status

Accepted. v0.1.0.

## Context

The schema versioning scheme needed an initial value. The `schema_version`
integer is included in both `manifest.json` and the `_schema` table in the
SQLite output.

## Decision

Initial `schema_version` = 1. Bumped only on breaking changes per spec rules
(see `CLAUDE.md` Schema versioning section).

## Consequences

**Positive.** Clear baseline for the analyser contract.

**Negative.** Pre-1.0, expect several bumps as the schema settles.

**Neutral.** README compatibility table maps `xray v0.1.0 -> schema_version 1`.
schema_version 2 was introduced by ADR 023.

## How to apply

`internal/model/` — `schema_version` constant. `README.md` — compatibility
table. Breaking changes require bumping the constant and updating the table.
