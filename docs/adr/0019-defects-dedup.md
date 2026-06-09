## Status

Accepted. v0.2.x.

## Context

When the same ticket reference (e.g. `PROJ-123`) appeared in both a PR's
title and its body, the defects extractor emitted two rows for the same
logical mention. Downstream queries counting distinct refs per PR
over-counted, and queries joining on `(repo, ticket_ref)` saw inflated
figures.

## Decision

When the same ticket reference appears in both a PR's title and body, one
row is emitted (not two). The `source` is set to `pr_title` if the ref
matched the title; else `pr_body`. Commit-message refs are unchanged — one
row per `(commit, ref)` — because a commit message is a single text
location.

## Consequences

**Positive.** Per-PR ref counts are accurate; join queries are not inflated.

**Negative.** None identified.

**Neutral.** Commit-message dedup semantics are unaffected.

## How to apply

`internal/connectors/github/defects.go` — change emission so PR-level
callers pass `(title, body)` together and the helper dedups before insert.
Update `internal/connectors/github/prs.go` to call the new helper with both
texts.
