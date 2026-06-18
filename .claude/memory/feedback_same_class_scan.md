---
name: same-class-scan
description: Before fixing a bug or feature, articulate its abstract shape and grep for siblings. The class is the unit of work, not the instance — peers in scope get fixed in the same PR.
metadata:
  node_type: memory
  type: feedback
  originSessionId: 2f62a88e-f634-4dae-81c9-a95d4caaa030
---

When fixing a bug or building a feature, **the class is the unit of work, not the instance**. Before writing the fix:

1. **Articulate the shape.** State the bug as a *pattern*, not an instance. "walk failure sets `prov.Errors[walk]` but not `prov.Errors[repo_languages]`" is an instance; the shape is *"error paths that should write to multiple provenance keys but only write to one."* If you cannot name the shape in one sentence, the framing isn't ripe — refine before continuing.

2. **grep for siblings.** Search the codebase for the *pattern*, not just the symbol. The grep is what makes the scan honest — memory of the codebase is wrong; the grep isn't. Common shapes in xray:

   - Provenance write coverage: `grep -rn 'prov\.Errors\[' internal/connectors/`
   - Permission-gated 403/404: `grep -rn 'StatusForbidden\|StatusNotFound' internal/connectors/`
   - Paginated walks: error paths near `pageInfo`, `opts.Page++`
   - Byte-size formatting: `grep -rn 'humanBytes\|formatSize\|MiB\|MB'`
   - Context propagation: `grep -rn 'http\.Get\|http\.Post\|http\.NewRequest[^WithContext]'`

3. **Decide per peer.** For every grep match:
   - Same bug, small fix → include in this PR's scope
   - Same bug, large fix → file *one* class-level issue ("apply X consistently across N sites"), not N issues
   - Different bug that happens to match the grep → ignore for this PR, document why in the PR description

The scan's output is a single sentence: *"shape is X; N peers found; M fixed here; K filed under #..."*. Carry this into the plan, task description, or PR body.

**Why:** Single-instance framing produces single-instance fixes; the next sibling regression files as a new issue, and the loop never converges. Issues #87, #88 worked because they were framed as classes from the start ("EndpointStatus across all connectors"). Issue #107 (walk error attribution) is an instance that almost certainly has peers — the shape, *"error path that should set multiple provenance keys but only sets one,"* is repeating across every connector that emits more than one row type per call.

**When to apply:**

- At the **start** of fixing an issue (informs the plan and PR scope)
- During `/ready` Step 4 (catches drift in sibling code that landed concurrently)
- When triaging a bug report before filing — *is this an instance of a class?* If yes, scope the issue to the class.

**Companion to** [[feedback_never_defer_bugs]] (peers found during scan get fixed in-session unless they are genuine new features) and [[feedback_ready_before_commit]] (the scan is part of /ready Step 4).
