# Session / issue start

Two modes:

- **`/start`** (no arg) — generic session start; load recent state and pick a direction
- **`/start <N>`** — start work on GitHub issue N

---

## Mode A: `/start` (no arg)

### Step 1: Recent state

```
git log --oneline -10
git status
```

Identify what shipped most recently. Note anything currently staged or modified —
those likely belong to another session's work; the guard-commit hook will keep
them out of any commit you make so don't unstage.

### Step 2: Open backlog

```
gh issue list --state open
gh issue list --state closed --limit 5
```

### Step 3: Recent ADRs

```
tail -50 tmp/adr.md
```

### Step 4: Baseline

```
make gates
```

If red, stop and surface the breakage to the user before starting anything new.

### Step 5: Direction

Based on the user's intent: pick an issue (`/start <N>` for the detailed path),
take a feature request, or answer a question. For non-trivial implementation
work, enter Plan mode.

---

## Mode B: `/start <N>`

Walk every step in order. Don't skip ahead even if the issue looks small —
the up-front context is the value.

### Step 1: Load the issue

```
gh issue view <N> --json title,body,state,labels,assignees,milestone,comments
```

If state is `CLOSED`: stop and tell the user. Don't reopen without explicit
permission.

### Step 2: Read labels first

Labels carry routing information. Recurring routings:

- `type:connector` — touches `internal/connectors/<X>`. Every connector change
  is reviewed against the **Non-negotiable invariants** in `CLAUDE.md`
  (read-only, no source content stored, provenance per-row, tokens never
  logged). Re-read that section before editing.
- `type:schema` — touches `internal/model` or the DDL. Check whether the
  change is *breaking* per `CLAUDE.md` → "Schema versioning"; if so, the
  `schema_version` integer must bump and `docs/schema.md` must be updated in
  the same commit.
- `type:core` — touches the core/connector seam. Core has no knowledge of
  any specific connector; connectors depend on core, never the reverse.
- `milestone:vX.Y.Z` — release-scoped work; check `CHANGELOG.md` for the
  in-flight section.
- `type:cli` — command surface; update `README.md` usage examples in the
  same commit.

### Step 3: Identify the work surface

From the issue body's **Scope** section, list the files and modules touched.
For each:

```
git log --oneline -10 -- <path>
```

This shows the recent context on each file — who else has been editing it,
what shipped most recently.

### Step 4: Cross-reference project docs

Surface relevant existing context the issue assumes:

- **Spec**: section of `docs/spec.md` that defines the behaviour being added
  or modified.
- **Schema**: section of `docs/schema.md` if the issue touches data shape,
  plus the DDL in `internal/model` directly.
- **ADR**: search `tmp/adr.md` for any prior decision touching the same
  surface — `grep -i <keyword> tmp/adr.md`.
- **Invariants**: the `CLAUDE.md` → "Non-negotiable invariants" section is
  the diff-review contract. Every connector change is checked against it.

### Step 5: Check dependencies

Scan the issue body for `#N` references. For each:

```
gh issue view <N> --json title,state
```

If a referenced issue is OPEN and blocks this work, flag the ordering
problem to the user.

### Step 6: Risk classification

Match the work against the **Non-negotiable invariants** in `CLAUDE.md`.
Common high-risk categories:

- Adding or editing a connector (read-only audit, provenance discipline)
- Schema change (breaking vs non-breaking; `schema_version` bump)
- Editing the HTTP transport / ratelimit wrapper
- Adding a new dependency (ADR entry required)
- Editing CI gates or `.claude/hooks/*`

If the work hits one of these, the invariants section is mandatory reading
before any edit. Quote the relevant clause back to the user.

### Step 7: Baseline

```
make gates
```

Required before starting. Don't begin work without a green baseline —
otherwise you can't tell whether your change broke something or it was
already broken.

### Step 8: Plan or proceed

- **Invariant-touching, OR multi-file, OR new connector, OR schema
  change**: enter Plan mode. Write the plan to the plan file. Get the
  user's approval before any edit.
- **Single-file, fully-scoped, sub-100-LOC change**: skip Plan mode.
  Create a task with `TaskCreate`, mark it in_progress, and implement.

### Step 9: Implement

Standing rules carry from `CLAUDE.md`:

- Commit with `git commit -m '<concise english>' -- path/to/file …`
  (pathspec form is enforced by `.claude/hooks/guard-commit.sh`).
- No `Co-Authored-By` trailer.
- No worktrees (enforced by `.claude/hooks/guard-worktree.sh`).
- When the work is done and `make gates` is green, commit and push without
  asking. Close the issue with `gh issue close <N> --comment "Closed in $(git rev-parse --short HEAD): <one-line>"`.

### Step 10: Update the cross-cutting docs

If the issue's `Acceptance` section listed any of these, update them in the
same commit (or a follow-up before pushing):

- `docs/schema.md` — for any schema change
- `docs/spec.md` — for any behavioural change
- `tmp/adr.md` — for any non-obvious decision locked in
- `CHANGELOG.md` — for cross-cutting work that lands in the next release
- `README.md` — for any user-visible command or output change

### Step 11: Close

When `make gates` is green locally and pushed, close the issue (see
Step 9). Surface the SHA to the user and ask whether to start the next
issue.
