## Status

Accepted. v0.1.0.

## Context

After core interfaces were defined, implementation work needed to fan out to
parallel agents. Worktrees were explicitly off the table by user preference.

## Decision

Implementation work fans out to per-milestone agents working in the same
checkout. Agents write files only; main commits. Per-directory ownership
(each connector in its own `internal/connectors/X/` directory) keeps races
to a minimum. `go.mod` is pre-populated to prevent concurrent edits.

## Consequences

**Positive.** Speed. Per-directory ownership minimises file-level conflicts.

**Negative.** If two agents accidentally touch the same file, the later
writer wins; main reviews the resulting diff before commit.

**Neutral.** No worktrees used.

## How to apply

Follow the per-directory ownership convention. Review diffs carefully when
merging parallel agent output.
