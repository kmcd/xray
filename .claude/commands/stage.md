# Stage

Prepare the working tree (and, if needed, the index) so the next `/commit` lands exactly the right paths — nothing from concurrent sessions, nothing partial.

This is the upstream half of `/commit`. The mechanical guard (`.claude/hooks/guard-commit.sh`) forces `git commit -- <paths>`, which scopes the commit to the listed paths regardless of what else sits in `.git/index`. That guarantee covers the *whole-file* case. Mixed-content files need an extra step — covered in Step 4.

## Step 1: Survey the working tree

```
git status --short
git diff --stat
```

List every modified, added, or deleted path. For each, decide whether it belongs to **this session's** work using the conversation context (files you read, files you wrote, the issue being worked on).

## Step 2: Bucket files

**Mine** — touched by this session, or clearly part of the same issue (impl, tests, docs, fixtures):
- Files this session edited or created
- Adjacent files in the same package/feature
- Test files for the above

**Not mine** — leave them alone:
- Files in unrelated packages
- Files this session never read or wrote
- When in doubt, leave it out

You do **not** need to unstage other sessions' files. Pathspec scoping at commit time will exclude them.

## Step 3: Whole-file changes — no staging needed

If every "mine" file is wholly yours (no foreign hunks mixed in), skip straight to `/commit`. There is nothing to do here.

`git commit -- <paths>` commits the **working-tree** content of those paths. The index is bypassed entirely. Pre-staging is pure ceremony for the whole-file case.

## Step 4: Mixed-content files — temporary-revert-stage-restore

If a file contains *both* your hunks and a concurrent session's unrelated hunks, partial staging (`git add -p`) **will not save you** — pathspec commit ignores the index for that path and commits the full working-tree content.

Do this instead:

1. **Edit the file to remove the foreign hunks**, leaving only your change in the working tree. Keep a scratch copy of the foreign hunks (paste into a comment buffer, or stash to `/tmp/foreign.patch` via `git diff <file> > /tmp/foreign.patch` before editing).
2. **Run `/commit`** for the now-clean file. Pathspec commit captures the working-tree state — your change only.
3. **Restore the foreign hunks** by re-editing the file (or `git apply /tmp/foreign.patch`). The foreign session's working-tree state is back where it was; they can commit their hunk when they're ready.

This isolates by *path-content* rather than by *path*, which is what the shared-index situation actually requires.

## Step 5: Confirm before handing off

```
git status --short
```

The "mine" set should be in the working tree, ready for `git commit -- <paths>`. Whether or not they're staged is irrelevant — the pathspec commit reads from the working tree.

When this looks right, run `/commit`.
