---
name: never-defer-bugs
description: Bugs surfaced during /ready — own-diff, adjacent, or pre-existing — get fixed in the same session before close. Filing follow-up issues for bugs is not allowed.
metadata:
  node_type: memory
  type: feedback
  originSessionId: 2f62a88e-f634-4dae-81c9-a95d4caaa030
---

**NEVER defer bugs.** Any bug surfaced during `/ready` — in your diff, adjacent to it, or pre-existing in code you read while reviewing — gets fixed in the same session before close. Filing a follow-up issue for a bug is not allowed.

Only genuine **scope additions** become new issues: new features, new connectors, new capabilities, design questions that require user input. The test: *is this missing correctness, or a new thing?* If correctness, fix now. If new thing, file.

**Why:** Deferring bugs to follow-up issues creates a never-ending chain. Every session ships imperfect code, files 3–5 follow-ups, the next session works the follow-ups and files 3–5 more. The cli-ux cluster filed #91–#95 in 32 seconds — five bugs the session introduced and refused to own. The 9f80fd2 handoff re-shipped the pattern: filed #107 (walk error attribution) and #108 (per-language byte-count test gap), both pre-existing bugs surfaced during review, both labelled "follow-up" — exact loophole the earlier rule allowed.

The loophole was: *"the bug existed before my diff, so it's a follow-up."* **Closed.** Pre-existing or not, if `/ready` surfaces it, you fix it. The whole point of the review steps is to find and close gaps before they multiply.

**How to apply:**

- Bug in code you just wrote → fix now, this session
- Bug in code adjacent to your diff → fix now, same session
- Bug in pre-existing code you happen to read during review → fix now, same session
- Bug surfaced by tooling (govulncheck, vet, lint, mutation tests) → fix now
- *New feature* you could add → file as follow-up
- *New capability* that requires architecture / user input on what to build → file as follow-up
- Unsure whether something is a bug or a new feature? → treat as bug, fix it now

Cost of fixing in-session: minutes. Cost of deferring: a new issue cycle, context switch, another `/ready`, another commit, another close — multiplied across every session that does it.

This rule **supersedes** the earlier `no-defer-own-bugs` framing, which carried the "pre-existing → file it" loophole. Pre-existing is no longer an exception.

Companion to [[feedback_close_after_smoke]] (smoke must validate before close) and [[feedback_ready_before_commit]] (`/ready` must pass before commit).
