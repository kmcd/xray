---
name: close-after-smoke
description: "ALWAYS invoke /ready before `gh issue close`. /ready's Step 6 smoke is the gate; gates + push alone are not enough. The protocol is enforced via /start Step 11 and CLAUDE.md → Gates."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 7462f281-5d0a-469a-98a9-e8935b9015dc
---

**Always invoke `/ready` before `gh issue close`.** Never close an issue on the strength of `make gates` green + push alone. Even when the implementation feels obviously correct, run `/ready` and let its smoke step verify against a realistic target before declaring done.

**Why.** During #71 I followed /start's old Step 9/11 (commit → push → close once gates were green) and called `gh issue close` while the 7-day posthog smoke was still running in the background. The user pulled me back: if the smoke had surfaced a regression, the issue would have already been marked done and the architectural change ungated. Closing should reflect "we verified this delivers," not "the code compiled and the unit tests passed."

**How to apply.**

- After `make gates` + `git push`, invoke `/ready` (`Skill ready`). Walk all six numbered steps; do not skip Step 6 (smoke).
- For empirically-measurable changes (`type:perf`, behavioural shifts, anything where wall-clock or row counts matter), the smoke step gates the close: no smoke result → no close.
- For pure refactor / docs / type-only changes, /ready's Step 6 itself says skip — but you still **invoke /ready** so the scope-sweep + deterministic review run.
- When a smoke runs in the background, *wait for the notification* before `gh issue close`. Quote the smoke's wall-clock and row counts in the close comment.

This is enforced structurally:

- [`.claude/commands/start.md`](../../../../src/xray/.claude/commands/start.md) Step 11 — "Always invoke `/ready` before closing"
- [`.claude/commands/ready.md`](../../../../src/xray/.claude/commands/ready.md) Step 6 — smoke gate for empirically-measurable changes
- [`CLAUDE.md`](../../../../src/xray/CLAUDE.md) → "Gates" — `make gates` and `git push` are necessary but not sufficient; close requires `/ready`

Related: [[feedback_push_without_asking]] (push is autonomous; close is not), [[feedback_gates_pattern]] (the three-tier gates hierarchy), [[reference_xray_artifacts]] (where smoke configs live).
