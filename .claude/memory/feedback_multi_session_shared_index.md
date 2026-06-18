---
name: multi-session-shared-index
description: "Multiple Claude sessions on one checkout share `.git/index`; pathspec commits ignore the index entirely and read working-tree content. Mechanically guarded by guard-commit.sh; mixed-content files need the temporary-revert-stage-restore pattern."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 31fad0e6-1ea6-481f-855b-f2913351b032
---

`xray` runs trunk-based on shared `main` with multiple concurrent sessions on the same working tree. `.git/index` is shared across sessions. A bare `git commit -m '...'` would sweep every staged file, including ones another session was preparing.

**The mechanical guard** (`.claude/hooks/guard-commit.sh`):

1. Rejects any `git commit` lacking an explicit `-- <paths>` separator.
2. Rejects `-a` / `--all` / combined short flags containing `a` / `--amend`.
3. Bypassed with `ALLOW_GIT_COMMIT_ALL=1` only when genuinely warranted.

**Why:** Multi-session checkouts share `.git/index`. A concurrent session's `git add` between your verification and your commit would silently pollute your commit's file list. Pathspec scoping makes the commit capture only the listed paths regardless of what else is in the index.

**How to apply:**

1. Always commit with `git commit -m "..." -- <files>`. The hook will block any other form.
2. After committing, verify with `git log -1 --stat` — the file list must match your pathspec.
3. **`git commit -- <paths>` commits the *working-tree* content of those paths, ignoring the index.** This means:
   - Pre-staging via `git add` is *irrelevant* — the index is bypassed.
   - `git add -p` to isolate hunks **does not work** — pathspec commit overrides the index.
   - For mixed-content files (your hunks plus a concurrent session's), use the **temporary-revert-stage-restore** pattern: edit the file to remove the foreign hunks (saving them to `/tmp/foreign.patch`), commit, then restore the foreign hunks. This isolates by *path-content* rather than by *path*.
4. The `/stage` skill (Step 4) documents the temporary-revert pattern; the `/commit` skill assumes the working tree already contains only what you intend to commit.
5. The hook is project-local. In a different checkout, the same discipline applies but won't be enforced.

See [[feedback_ready_before_commit]] for the gate ordering (`/ready` → `/commit`).
