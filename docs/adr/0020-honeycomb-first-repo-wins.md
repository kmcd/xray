## Status

Accepted. v0.2.x. Documented as a known v1 limitation.

## Context

Honeycomb has no per-repo concept. Deployment markers extracted from a
Honeycomb dataset cannot be reliably attributed to a specific repo. Options
considered: tag every marker to every repo (inflates counts), tag to the
first repo seen (honest about the limitation), or skip Honeycomb entirely.

## Decision

No code change. The connector continues to attribute all markers to the first
repo seen. The limitation is surfaced in `CHANGELOG.md` "Known limitations"
and `docs/schema.md` `deploys` row notes.

## Consequences

**Positive.** No artificial inflation of deploy counts across repos.

**Negative.** All deploys attributed to the first repo, which is inaccurate
for multi-repo configurations. A real fix would need the Honeycomb dataset to
carry a per-repo dimension.

**Neutral.** Limitation is documented so analysers can account for it.

## How to apply

Docs only — `CHANGELOG.md`'s v0.2.0 section and `docs/schema.md`'s `deploys`
notes. Code unchanged.
