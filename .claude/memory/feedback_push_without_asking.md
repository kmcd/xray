---
name: feedback-push-without-asking
description: "When asked to commit, also push — don't defer the push back to the user."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 9e171e63-db97-416b-9970-d582c0c3523e
---

When the user asks to commit, push as part of the same action. Don't append "not pushed — you handle that" or similar deferrals; just `git push` after the commit succeeds.

**Why:** User explicitly corrected this behavior. Deferring the push back is friction the user has chosen not to want.

**How to apply:** After any successful `git commit` triggered by the user (whether they said "commit" or "ship" or "land"), follow with `git push` to the tracked remote in the same flow. Only ask if there's a real reason to pause — e.g. force-push to main, or no upstream configured.
