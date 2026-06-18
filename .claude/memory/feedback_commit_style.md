---
name: feedback-commit-style
description: "User's commit message conventions — no Claude co-author trailer, concise plain English."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 334ab2b5-e8b4-4939-93e9-032dc922183e
---

Do not add `Co-Authored-By: Claude` (or any Claude trailer) to commits.
Use concise, natural English commit messages — imperative mood, no emojis, no PR-style padding.

**Why:** User stated explicitly when starting the xray project work; they handle attribution themselves and prefer terse log lines.

**How to apply:** Every `git commit` on every project. When writing the message, aim for one short subject line; add a brief body only if the change isn't self-explanatory from the diff. Skip the standard Claude Code co-author footer the harness suggests for the `Bash` git workflow.
