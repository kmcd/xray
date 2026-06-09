## Status

Accepted. v0.1.0.

## Context

Commit message style needed to be specified to keep the git log consistent
and to avoid noise from CI-generated trailers or emoji.

## Decision

Concise English imperative commit messages, no Claude co-author trailer, no
emojis. One-line subject; body only when not self-evident from the diff.

## Consequences

**Positive.** Clean, scannable git log.

**Negative.** None identified.

**Neutral.** Also captured in personal memory for AI coding assistants.

## How to apply

All commits to this repo. Enforced by `bin/ship` hook convention and personal
memory.
