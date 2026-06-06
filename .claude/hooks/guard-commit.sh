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
# staged. Adapted verbatim (logic) from gauge_intelligence.
#
# Rules:
#   1. `git commit` MUST have an explicit `--` followed by at least one pathspec.
#   2. `-a` / `--all` / `-am` / `-ma` are blocked.
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

SUBCMDS = re.split(r"(?:&&|\|\||;|\||&)(?!\w)", cmd)

def is_assignment(tok: str) -> bool:
    if "=" not in tok:
        return False
    return tok.split("=", 1)[0].isidentifier()

all_tokens = []
for sub in SUBCMDS:
    try:
        all_tokens.extend(shlex.split(sub, posix=True))
    except ValueError:
        sys.exit(0)

if any(t == "ALLOW_GIT_COMMIT_ALL=1" for t in all_tokens):
    sys.exit(0)

def find_git_commit_start(tokens):
    """Yield (start_index) where 'git commit' appears as a command-start."""
    i = 0
    while i < len(tokens):
        while i < len(tokens) and is_assignment(tokens[i]):
            i += 1
        if i + 1 < len(tokens) and tokens[i] == "git" and tokens[i + 1] == "commit":
            yield i + 1  # index of 'commit'
        i += 1

for sub in SUBCMDS:
    try:
        toks = shlex.split(sub, posix=True)
    except ValueError:
        continue
    for cmt in find_git_commit_start(toks):
        rest = toks[cmt + 1 :]
        joined = " ".join(rest)
        # Block --amend / -a / --all / -am / -ma
        for bad in ("--amend", "-a", "--all", "-am", "-ma", "-Aa", "-aA"):
            if bad in rest:
                print(
                    f"Blocked: `git commit {bad}` is not allowed.\n\n"
                    "This project uses single-shared-tree workflow. Bare commits sweep all\n"
                    "staged files; --amend rewrites the previous (potentially another\n"
                    "session's) commit. Use:\n"
                    "    git commit -m '<msg>' -- path/to/file1 path/to/file2\n\n"
                    "Bypass once with: ALLOW_GIT_COMMIT_ALL=1 git commit ...",
                    file=sys.stderr,
                )
                sys.exit(2)
        if "--" not in rest:
            print(
                "Blocked: `git commit` without `-- <pathspec>` is not allowed.\n\n"
                "Concurrent sessions can stage files into the shared index.\n"
                "Scope every commit to the paths it should capture:\n"
                "    git commit -m '<msg>' -- path/to/file1 path/to/file2\n\n"
                "Bypass once with: ALLOW_GIT_COMMIT_ALL=1 git commit ...",
                file=sys.stderr,
            )
            sys.exit(2)
        dash_idx = rest.index("--")
        if dash_idx == len(rest) - 1:
            print("Blocked: `git commit ... --` with no pathspec.", file=sys.stderr)
            sys.exit(2)

sys.exit(0)
PY
