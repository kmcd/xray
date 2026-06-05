# Completion gate

Run this checklist after finishing implementation work, before declaring done. Do not skip steps. Do not summarise results until every step passes.

## Step 1: gates

Run the full local gate suite:

```
make gates
```

This runs lint, govulncheck, and the test+coverage pair (the same three jobs CI runs on push). If any fail, fix the issue and re-run. Do not proceed until all three are green locally — CI will fail the same way otherwise.

## Step 2: self-review (deterministic)

Review the working-tree diff against [`.claude/diff_review.md`](.claude/diff_review.md). Use judgement — not every criterion applies to every diff. The categories that always apply:

- **Schema invariants** (when DDL or `internal/model` changes)
- **Connector contract** (when any `internal/connectors/X` changes)
- **Read-only and provenance discipline**
- **No secrets in logs**

Fix anything you spot. Do not just report it.

## Step 3: self-review (inferential)

Invoke the `code-review` skill against the working-tree diff:

```
Skill code-review
```

Treat its output as a peer review, not a vote: address every concern with a fix or an explicit "won't fix because ...". Run `make gates` again after any change.

## Step 4: scope sweep

Force-compare the work against the original request. Completion criteria are tempted to optimise for "the code I wrote is clean" rather than "the request is satisfied". Produce three lists and surface them to the user:

- **Asked** — re-read the originating request as written (issue body, the user message that started the work, the GitHub comment thread). Enumerate every concrete item. Do not paraphrase from memory.
- **Done** — what the diff actually changed. Cross-reference against Asked.
- **Deferred (with reason)** — anything in Asked but not Done. Name each item explicitly; do not bury. If empty, say so.

Then run the same-class scan — don't just make this instance go away, apply the fix or feature consistently wherever the same shape exists in the codebase:

- What is the abstract shape of what I just changed — a missing index, a new schema column, a connector field, a parsing rule, a permission-gated endpoint?
- Grep for other instances of that shape. Use actual identifiers (sibling connector names, sibling model fields) rather than relying on memory.
- For every peer that has the same shape: fix it in this commit when small, file a tracking issue when not, name it in the handoff when out of scope.

## Step 5: docs

Skip for pure bug fixes or refactors with no user-visible behaviour change. Otherwise:

- `README.md` — usage examples, install instructions, compatibility table
- `docs/schema.md` — when `internal/model` or the DDL changes
- `CLAUDE.md` — when a settled assumption changes (rare; bumps the spec)
- `tmp/adr.md` — record non-obvious decisions made during this work

Fix any gaps. Do not just report them.

## Step 6: handoff

Summarise to the user in two parts:

1. What changed — one paragraph, no headers.
2. What was deferred — bullet list with reasons. If empty, say so.

Do not offer follow-up tasks; end the message.
