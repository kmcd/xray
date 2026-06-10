#!/usr/bin/env bash
# PreToolUse hook (matcher: Bash). Hard gate against shared-index contamination.
# Requires every `git commit` to use the `-- <pathspec>` form so the commit
# captures only the listed paths, not whatever a concurrent session has also
# staged into `.git/index`.
#
# Background: multi-session Claude Code sessions share `.git/index` because
# they share the working tree. A bare `git commit -m '...'` would sweep all
# staged files; pathspec scoping (`git commit -m '...' -- path/to/file`)
# scopes the commit to exactly the listed paths even if other files are
# staged. Adapted from gauge_intelligence.
#
# Rules:
#   1. `git commit` MUST have an explicit `--` followed by at least one pathspec.
#   2. `-a` / `--all` / combined short flags containing `a` (`-am`, `-avm`, ...)
#      are blocked.
#   3. `--amend` is blocked.
#
# Escape hatch: prefix the command with `ALLOW_GIT_COMMIT_ALL=1`.

set -eo pipefail

INPUT=$(cat 2>/dev/null || echo '{}')

python3 - <<'PY' "$INPUT"
import json, shlex, sys, re

payload = json.loads(sys.argv[1] or '{}')
if payload.get("tool_name") != "Bash":
    sys.exit(0)

cmd = payload.get("tool_input", {}).get("command", "")
if not cmd:
    sys.exit(0)

# Split on top-level shell separators so each sub-command is checked.
SUBCMDS = re.split(r"(?:&&|\|\||;|\||&)(?!\w)", cmd)

def is_assignment(tok: str) -> bool:
    if "=" not in tok:
        return False
    return tok.split("=", 1)[0].isidentifier()

# Tokenise everything first so the escape envvar can sit anywhere.
all_tokens = []
for sub in SUBCMDS:
    try:
        all_tokens.extend(shlex.split(sub, posix=True))
    except ValueError:
        sys.exit(0)

if any(t == "ALLOW_GIT_COMMIT_ALL=1" for t in all_tokens):
    sys.exit(0)

def find_git_commit(toks):
    """Return the slice of toks after `commit` if this is `git [opts] commit ...`, else None."""
    i = 0
    while i < len(toks) and is_assignment(toks[i]):
        i += 1
    if i >= len(toks) or toks[i] != "git":
        return None
    i += 1
    # Skip git-level options like `-c key=val`, `--git-dir=X`, `-C path`.
    while i < len(toks) and toks[i].startswith("-"):
        opt = toks[i]
        if opt in ("-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path"):
            i += 2
        else:
            i += 1
    if i >= len(toks) or toks[i] != "commit":
        return None
    return toks[i + 1:]

def check(commit_args):
    """Returns (blocked_reason, advice) or (None, None)."""
    # Split args at the `--` separator: tokens before it are opts, after are pathspecs.
    if "--" in commit_args:
        sep_idx = commit_args.index("--")
        opts = commit_args[:sep_idx]
        pathspecs = commit_args[sep_idx + 1:]
    else:
        opts = commit_args
        pathspecs = []

    # Block --amend (opts side only — `--amend` as a pathspec is fine).
    for tok in opts:
        if tok == "--amend":
            return ("--amend",
                    "Amending in a shared-index checkout risks amending a different "
                    "session's commit. Create a new commit instead.")

    # Block -a / --all / combined short flags containing `a`.
    for tok in opts:
        if tok in ("-a", "--all"):
            return (tok,
                    "`-a`/`--all` sweeps all modified tracked files, defeating "
                    "pathspec scoping. List the files explicitly after `--`.")
        if re.fullmatch(r"-[A-Za-z]+", tok) and "a" in tok[1:]:
            return (tok,
                    "Combined short flag includes `a` (commits all modified tracked "
                    "files). List the files explicitly after `--`.")

    # Require `--` separator with at least one pathspec.
    if not pathspecs:
        return ("missing `-- <pathspec>`",
                "`git commit` must scope to explicit paths so a concurrent session's "
                "staged files don't sneak in.")

    return (None, None)

for sub in SUBCMDS:
    try:
        toks = shlex.split(sub, posix=True)
    except ValueError:
        continue
    commit_args = find_git_commit(toks)
    if commit_args is None:
        continue
    reason, advice = check(commit_args)
    if reason is None:
        continue

    sys.stderr.write(f"""Blocked: `git commit` rejected — {reason}.

{advice}

Required form:
  git commit -m "<subject>" -- <file1> <file2> ...

This is the shared-index safeguard. Multiple Claude sessions share `.git/index`;
pathspec scoping is the only mechanical defence.

Bypass (rarely needed): prefix with `ALLOW_GIT_COMMIT_ALL=1`.

Ref: .claude/hooks/guard-commit.sh, .claude/commands/commit.md, .claude/commands/stage.md
""")
    sys.exit(2)

sys.exit(0)
PY
