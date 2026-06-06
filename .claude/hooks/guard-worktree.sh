#!/usr/bin/env bash
# PreToolUse hook (matcher: EnterWorktree). Blocks any invocation of the
# EnterWorktree tool. This project's workflow is sequential on shared `main` —
# per-agent worktrees break the shared-tree contract.
#
# Adapted verbatim from gauge_intelligence's guard-worktree.sh.

set -eo pipefail

cat >/dev/null

cat >&2 <<'EOF'
Blocked: EnterWorktree is forbidden in this project.

Workflow rule: sequential on shared `main`. No worktrees, no per-agent
branches, no per-agent PRs. Agents share the working tree on `main` so the
orchestrator can run `bin/ready` once at convergence.

If you found yourself wanting to isolate, the right move is:
  - surface the conflict in your report and stop, or
  - if you are the orchestrator, sequence the dependent agent after the
    conflicting one completes — not in parallel.
EOF
exit 2
