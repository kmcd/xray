## Status

Accepted. 2025.

## Context

Non-obvious design choices during xray's build-out need a durable record so
that future contributors (human or AI) understand what was decided and why,
without having to reconstruct intent from commit messages or code comments.

## Decision

We maintain a running log of architecture decision records (ADRs) for every
non-trivial design choice. Each entry states the decision, the rationale, and
alternatives weighed. Records are append-only; supersede with new entries
rather than editing in place.

## Consequences

**Positive.** Decisions are traceable. Parallel agents working in the same
checkout can reference prior decisions by number rather than re-litigating
them.

**Negative.** Adds a small overhead per decision. Empty entries (trivial
choices that don't warrant documentation) are noise.

**Neutral.** ADRs are stored in `docs/adr/` with stable `NNNN-slug.md`
filenames so cross-references survive renames.

## How to apply

New decisions: add a file under `docs/adr/` following the `NNNN-kebab-slug.md`
naming convention, increment the number, and update `docs/adr/README.md`.
Reference ADRs from code comments, `CLAUDE.md`, and `docs/` with relative
links to the file.
