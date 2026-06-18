---
name: clean-clone-check
description: "Before pushing to a shared branch, verify the commit doesn't reference uncommitted symbols by checking what was actually staged — `make gates` local-green is necessary but not sufficient."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: e0fc6ad3-94ef-440a-945d-a3194e18603c
---

When committing pathspecs of files you've edited, those files may carry changes from a parallel session's working tree on top of your own. `git commit -- path` commits the **current working-tree state** of the path, not just the hunks you authored. The commit can end up referencing symbols defined in files you didn't include in the pathspec.

**Why:** This shipped a broken `origin/main` (xray `37c46e7`). The other session was working on #86 (graceful Ctrl-C) and had `cmd/xray/run.go` + `internal/run/run.go` modified with references to `OnTempDir`/`tmpDirRef`/`interruptedResult`/`newInflightTracker` — symbols defined in their **untracked** `interrupt.go` and modified-but-uncommitted `options.go` + `main.go`. When I committed my #91–#95 fold-back with `-- internal/run/run.go cmd/xray/run.go ...`, git included their hunks too. `make gates` passed locally because the working tree had the definitions. A clean clone of origin/main failed with `undefined: interruptedResult`.

**How to apply:**
1. Before `git commit`, run `git diff --staged <path>` (or `git diff --cached`) for any file you commit by pathspec. If you see hunks you didn't author, decide explicitly whether they belong in this commit.
2. `make gates` green is the necessary check; **"clean clone from origin builds"** is the real test of record for any push to a shared branch. The fastest version: `cd /tmp && git clone --depth 1 <url> tmp-check && cd tmp-check && go build ./...`. Worth doing when the working tree has known foreign edits (other sessions, partially-applied changes).
3. When picking pathspecs in a noisy working tree, prefer narrowing to specific paths you authored over `-a` / no pathspec. But narrow pathspecs do not protect you from foreign hunks inside the files you DO include — see step 1.

See also: [[gates-pattern]] — the gate hierarchy already says `bin/ship` runs `make gates` pre-push; this lesson adds that clean-clone-build is a check the local gates can't perform.
