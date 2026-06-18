---
name: feedback_changelog
description: "Always add a CHANGELOG [Unreleased] entry for user-visible behaviour changes before closing an issue"
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 64a9921c-d1c2-4dae-affe-c96209bc6a02
---

Any commit that adds a new column, new connector behaviour, new CLI flag, or any other user-visible change must have a corresponding entry under `## [Unreleased]` in `CHANGELOG.md` before closing the issue — not as an afterthought once the user points it out.

**Why:** Missed during #180 close; user had to prompt for it. The CHANGELOG entry is part of the definition of done for user-visible work, the same as schema.md or spec.md updates.

**How to apply:** In `/ready` Step 5 (docs), check `CHANGELOG.md` alongside schema.md/spec.md. If the diff contains any new table, column, connector behaviour, CLI command, or flag: add an entry under `[Unreleased]`. Commit it in the same pathspec commit as the work, or as an immediate follow-up before pushing.
