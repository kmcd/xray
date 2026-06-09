## Status

Accepted. v0.2.x.

## Context

The deploy-rollback heuristic detected rollbacks using:
`D[i].commit_sha == D[i-2].commit_sha AND D[i].commit_sha != D[i-1].commit_sha`.
This false-positived on routine re-deploys of a green commit — canary
advance, autoscaling rollouts, blue/green flips back. None of those are
rollbacks.

## Decision

The heuristic now additionally requires `D[i-1].status != "success"`. The
deploy *being* rolled back is the one that failed; requiring the predecessor
to be non-success gates the heuristic to the actual rollback pattern.

## Consequences

**Positive.** Routine re-deploys of green commits no longer trigger
`is_rollback`. The heuristic fires only when the predecessor deploy failed.

**Negative.** The heuristic remains a heuristic; an explicit `rolled_back`
signal from a deploy provider, when available, is authoritative.

**Neutral.** Table-driven tests should cover rollback (prior deploy failed)
vs. re-deploy (prior deploy succeeded).

## How to apply

`internal/postprocess/postprocess.go` — `linkDeployRollbacks`. Add test cases
distinguishing rollback (prior deploy failed) from re-deploy (prior deploy
succeeded).
