---
name: feedback-release-intro
description: Skip the release intro paragraph confirmation — auto-accept the draft and continue
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 837530db-b224-4257-bcbf-ea8a07955bbb
---

Auto-accept the drafted intro paragraph in `/release` Step 2; do not prompt the user to confirm or edit it.

**Why:** User explicitly asked not to be prompted for this step.

**How to apply:** In `/release`, draft the intro paragraph, insert it directly into CHANGELOG, and continue to Step 3 without calling `AskUserQuestion`.
