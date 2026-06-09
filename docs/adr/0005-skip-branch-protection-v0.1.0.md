## Status

Accepted. v0.1.0. Deferred to post-v0.1.0.

## Context

Issue #8 (branch protection on main) was part of the M0 milestone. Branch
protection requires a PR-based workflow, which conflicts with ADR 001's
direct-to-main approach.

## Decision

Branch protection is not configured in v0.1.0. The issue is closed with a
"deferred to post-v0.1.0" comment.

## Consequences

**Positive.** No conflict with the direct-to-main workflow.

**Negative.** No automated review gate on `main`. CI is the only safety net.

**Neutral.** Force-push protection should be enabled once past v0.1.0 thrash.

## How to apply

N/A — deferred decision. Revisit when moving to a PR-based workflow.
