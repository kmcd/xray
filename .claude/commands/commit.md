# Commit

Commit, push, close — for files this session touched, and only those.

This project runs trunk-based on shared `main` with multiple concurrent sessions. The working tree and `.git/index` are shared. A bare `git commit -m '...'` would sweep every staged file, including ones another session was preparing. Pathspec scoping is the safeguard.

**Mechanical guard:** `.claude/hooks/guard-commit.sh` rejects any commit that lacks an explicit `-- <paths…>`, plus `-a` / `--all` / `--amend`.

## Step 1: identify *your* files

```
git status --short
git diff --stat
```

Read the conversation: which files did **this session** edit or create? List them explicitly. Ignore anything else — it belongs to another session and pathspec scoping will keep it out.

If unsure whether a file is yours, check `git diff <file>` against what you remember writing. Don't guess.

## Step 2: group by logical unit

One commit per coherent change. If your files span two issues, that's two commits. Don't bundle unrelated work to save typing.

## Step 3: write a message

- Concise, natural English, imperative ("Add ratelimit budget", not "Added").
- **No** Conventional-Commits prefix (`feat:`, `fix:`, `chore:`).
- **No** `Co-Authored-By` trailer.
- Issue refs inline ("Close #87") when relevant, not as trailer.
- Body optional; if present, explain *why* the change exists, not *what* it does.

Examples:

```
ratelimit: budget snapshot + predictive primary-low-water warning. Close #82.
repo-health: LICENSE, SECURITY, CONTRIBUTING, CoC, GitHub templates
github: record EndpointStatus for releases, pr_template, repo_metadata, contributors
```

## Step 4: commit with pathspec

Single-line subject — inline `-m`:

```
git commit -m "<subject>" -- path/to/file1 path/to/file2 path/to/dir/
```

Multi-line message — HEREDOC silently fails through the Bash tool, use `Write` + `-F`:

```
# write the message
Write /tmp/commit_msg.txt
# then commit
git commit -F /tmp/commit_msg.txt -- path/to/file1 path/to/file2
rm /tmp/commit_msg.txt
```

Directory pathspecs (`.github/`) include everything beneath them — useful and safe when you own the whole subtree.

## Step 5: Verify scope

```
git log -1 --stat
```

Confirm the commit captures exactly your files and nothing else. If something foreign got in: `--amend` is blocked. Revert and re-commit with tighter pathspec:

```
git reset HEAD~1     # leaves files unstaged
git commit -m "..." -- <tighter paths>
```

## Step 6: Push

When the user asks you to commit, push too — don't defer it back. Standing rule.

```
git push
```

## Step 7: close the issue (if the work closes one)

Only after `/ready` reported clean — `make gates` green + push are necessary but not sufficient. See `CLAUDE.md` → Gates.

```
gh issue close <N>
```

If `/ready` hasn't run for this work, stop and run it before closing.
