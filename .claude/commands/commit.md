# Commit

Commit only files related to the current work. Multiple Claude Code sessions may be active
on `main`, so the shared `.git/index` may carry files that belong to other work.

**Mechanical safeguard:** `git commit` is gated by `.claude/hooks/guard-commit.sh`, which
requires every commit to use the `-- <pathspec>` form. The hook also blocks `-a`/`--all`/
`--amend`. The `Co-Authored-By` trailer is also disallowed by project convention.

## Step 1: Identify your session's files

```
git status
git diff --stat
```

Look at the conversation context to identify which files belong to **this session's work**.
Ignore anything that looks like another session's work — pathspec scoping keeps it out of
your commit; you don't need to unstage it.

## Step 2: Group by issue or logical unit

If files clearly belong to a single issue, make one commit. If files span multiple issues,
plan one commit per issue.

## Step 3: Commit using pathspec form

For each group, in a logical order:

```
git add path/to/file1 path/to/file2
git commit -m "<concise natural english subject>

<optional body — why, not what>" -- path/to/file1 path/to/file2
```

Subject conventions:

- Concise natural English. **No** Conventional-Commits prefix (`feat:`, `fix:`, …).
- **No** `Co-Authored-By` trailer.
- Imperative mood ("Add ratelimit budget", not "Added").
- Issue references inline when relevant ("Close #8") — not as a trailer.

The HEREDOC-in-`-m` form silently fails through the Bash tool. For multi-line
messages, write to `/tmp/commit_msg.txt` with Write and use `-F`:

```
git commit -F /tmp/commit_msg.txt -- path/to/file1 path/to/file2
rm /tmp/commit_msg.txt
```

## Step 4: Verify

```
git status
git log -1 --stat
```

Confirm the commit captures exactly the intended files, and that no other session's files
crept in. If they did: amend is blocked — revert with `git reset HEAD~1` (which leaves
files unstaged) and re-commit with a tighter pathspec.

## Step 5: Push

When the user asked you to commit, push too — don't defer it back. Standing
rule from auto-memory.

```
git push
```
