## Status

Accepted. v0.1.0.

## Context

The Ruby `gauge_intelligence` project had developed several agentic
infrastructure patterns that proved their worth. Adapting them for xray was
considered.

## Decision

Brought across three patterns from the Ruby project:
- `.claude/commands/ready.md` — `/ready` completion-gate slash command
  (gates + diff review + scope sweep).
- `.claude/diff_review.md` — auto-applied review criteria (schema parity,
  connector contract, HTTP boundary, file IO, test shape, style).
- `.claude/agent_prompt_template.md` — verbatim clauses to paste into
  parallel agent dispatches. Each clause is named after a specific failure
  mode from the v0.1.0 build.
- `bin/ship` — thin wrapper over `make gates`.

**What was skipped.** `guard-commit.sh` (no shared-index multi-session risk
in solo-on-main), `guard-ck.sh` (no analog heavy CI gate to block), in-repo
`.claude/memory/` (global memory already covers this), and the editorial /
domain-specific clauses.

## Consequences

**Positive.** Patterns earned their keep in gauge. Agent template makes
forcing functions explicit rather than recalled for the next fan-out.

**Negative.** Small maintenance overhead per pattern.

**Neutral.** Adapting for Go was small.

## How to apply

`.claude/commands/ready.md`, `.claude/diff_review.md`,
`.claude/agent_prompt_template.md`, `bin/ship` — already in place. Run
`/ready` before closing any issue.
