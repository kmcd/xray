---
name: feedback_ready_before_commit
description: Run /ready before committing and pushing — not after
metadata: 
  node_type: memory
  type: feedback
  originSessionId: a03d956d-96e4-4d40-976a-6fa400119361
---

Run `/ready` before committing. Always commit via the `/commit` skill, not raw `git commit`. The correct order is:
1. `/ready` (full completion gate — gates + review + smoke)
2. `/commit` (the skill, not `git commit` directly)
3. Push happens as part of `/commit`

**Why:** The smoke step in `/ready` catches regressions unit tests miss. Committing before `/ready` passes risks landing broken work. `/commit` enforces the pathspec commit style and push in one step.

**How to apply:** This applies to every change, including small fixes like the bugsnag ConfigDepth fix — no exceptions for "it's only a few lines." [[close_after_smoke]]
