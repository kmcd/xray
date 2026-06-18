---
name: prose-preload
description: "For any prose-touching work in xray (.md edits), read the Vale rule files and the style guide BEFORE drafting — preventive loop, not reactive gate."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: ccbe82bd-d620-436e-9479-7aa1ffc0639d
---

For any prose-touching work in xray — README, CHANGELOG, CONTRIBUTING, SECURITY, CLAUDE.md, or any `docs/*.md` — read these three files BEFORE writing any prose:

- `.vale/styles/xray/AntiLLM.yml` — substitution list (delve, leveraging, comprehensive, robust, seamless, in the realm of, harness the power, …)
- `.vale/styles/xray/Hedging.yml` — banned hedging tokens (simply, just, obviously, clearly, of course, …)
- `docs/style-guide.md` — load-bearing phrases (do not soften security guarantees), severity policy, override conventions

**Why:** Running `make prose` after writing is reactive — write → fail → rewrite. The user wants preventive loading so the rules shape the draft, not just gate it. The xray surface is small enough (≤10 prose files) that pre-loading costs a tiny read budget and saves the reactive loop.

**How to apply:** Before the first Edit/Write on a `.md` file in a session, Read the three files above. If they're already loaded earlier in the session, skip — but if it's been many turns or compaction has happened, re-read. Then draft. `make prose` is still the backstop, not the primary check.

**Caveat (from gauge_intelligence's `vale_promotion_policy.md`):** Token-level rules are easy to route around — an agent shown `delve → examine` writes "explore" or "investigate" instead and dodges the rule while producing the same high-register filler. The defence is to internalise the *register* the rules are pointing at (terse, no marketing, no hedging, customer-facing-trust voice), not just memorise the swap list. See also [[xray-baseline]] for the customer-trust register.

Cross-references: [[ready-before-commit]] (make prose is part of the gate), [[clean-clone-check]] (docs commits still need pathspec discipline).
