# xray — agent working notes

`xray` is a read-only Go CLI that extracts engineering metrics from a
client's systems (git history, GitHub PRs and reviews, CI builds, error
trackers, observability) into a single portable `.tar.gz` containing a
SQLite database and a JSON manifest. The artifact contains no source code
and no secrets.

This file is the agent-facing constraint sheet — what's baked in, what's
closed off, what must not be violated. The full product spec (commands,
config schema, output artifact, connectors, behaviour) lives in
[`docs/spec.md`](docs/spec.md); the schema reference lives in
[`docs/schema.md`](docs/schema.md). Verbatim clauses to paste into parallel
agent prompts live in [`.claude/agent_prompt_template.md`](.claude/agent_prompt_template.md);
diff-review criteria live in [`.claude/diff_review.md`](.claude/diff_review.md);
the completion gate lives in [`.claude/commands/ready.md`](.claude/commands/ready.md).

Cross-tool entry point: [`AGENTS.md`](AGENTS.md).

---

## Settled context

Baked-in assumptions for the implementation, not decisions to revisit:

- The config file is generated, edited, and run by the client in their own
  environment. It is never committed to git, never shared back to the
  consultant.
- Credentials live in the config file alongside non-secret config. The file
  never leaves the client machine, so the security delta vs env-injection is
  negligible.
- Repos are cloned from `owner/repo` slugs by the tool. Local-checkout mode
  is explicitly not supported.
- Config file format: TOML.
- Output: a single `.tar.gz` containing a SQLite database and a JSON manifest.
- The artifact contains no source code and no secrets.
- All measurement is team and system level. No individual-developer
  identifiers in any output table. Enforced in the schema, not by discretion.
- Connectors stay read-only. The tool never writes to any remote system.
- No ML/NLP models in the extractor binary. The single-static-binary,
  checksum-verifiable security-review story is the whole point of the Go
  choice and is not negotiable.

### Governing principle

Capture richly at extract time. The extractor is the only thing that touches
the client environment; runs are idempotent and full; re-extraction means
going back to the client — the expensive path. Capture rich neutral data
once and let the downstream analysis layer decide what it uses and what it
ignores.

---

## Doors closed

Settled exclusions with reasoning, listed to forestall their reintroduction:

- **Raw commit message bodies** are not stored. Parse at extract time, emit
  structured columns and rows, discard the text.
- **Raw PR body text** is not stored. Parse at extract time, emit structured
  columns, discard the text. Closing-issue references use GitHub's
  `closingIssuesReferences` GraphQL field where available, sidestepping body
  parsing for that signal.
- **Raw diff / patch text** is not captured. Per-file numstat
  (additions/deletions/path) is the line; patches are source content and
  stay in the client environment.
- **Sentiment analysis** is out. Weak signal on technical text, the
  underlying phenomena are measured directly elsewhere (change failure rate,
  incident rate, revert rate, review latency), and shipping an NLP model
  would break the static-binary constraint.

---

## Non-negotiable invariants

Every connector / extractor change is reviewed against these. They restate
the constraints implied by Settled context + Doors closed in the form a
diff is checked against; the longer rationale lives in [`docs/spec.md`](docs/spec.md)
under "Behaviour".

- **Read-only.** No `POST`, `PATCH`, `PUT`, or `DELETE`. Audit any
  go-github method whose name starts with `Create`, `Update`, `Delete`,
  `Edit`, `Add`, or `Remove`. The tool issues only read calls even when
  granted write scope.
- **No source content stored.** `commit_files` is numstat only; `file_metrics`
  is byte-scan stats only; `harness_artifacts.content` is empty unless
  `capture_harness_content = true`. PR/commit/review bodies parse at
  extract time, contribute structured columns (lengths, counts, marker
  flags), and **never persist** — the body variable drops out of scope in
  the same function.
- **Permission-gated endpoints record absence.** A 403 / 404 on an endpoint
  sets `prov.Endpoints[<endpoint>] = EndpointStatus{Accessible: false, Reason: "..."}`
  and skips rows for that endpoint. The analyser reads
  absence-because-inaccessible as **unknown**, not **no signal**.
- **Provenance is per-row.** Every successful insert increments
  `prov.RowsReturned[<table>]`; every error appends to
  `prov.Errors[<context>]` and continues — a per-row error does not abort
  the connector. Pagination interruptions set
  `prov.PaginationComplete = false`.
- **Tokens never logged** at any level.
- **No new dependencies** without an ADR entry. `go.mod` is the contract.
- **Team-level only.** No individual-developer identifiers beyond opaque
  `*_handle` strings used solely for linkage. No per-individual
  aggregation tables.

---

## Non-goals (v1)

- No local-checkout mode. Tool always clones.
- No incremental extraction. Each run is full within the window.
- No source-content inspection. Git metadata, structural file metrics, and
  numstat only. Diff text, comment-line counts, TODO/FIXME counts, and
  per-language semantic analysis are out.
- No individual-developer rankings or per-individual rollups in any output
  table.
- No web UI, no daemon, no scheduled mode. CLI only.
- No ticket-system connectors in v1. Text-reference extraction from
  PR/commit bodies populates `defects`; direct integrations (Jira, Linear,
  etc.) are added per-engagement when closure/reopen data is genuinely
  needed.

---

## Schema versioning

`schema_version` is an integer included in both `manifest.json` and a
`_schema` table in the SQLite. Monotonically increasing.

`schema_version` is the **analyser contract**. Binary semver is decoupled
— a single major version of the binary may emit multiple schema versions;
the README's compatibility table maps the two. Do not conflate them.

**Breaking (bumps `schema_version`):**

- Removing a table or column
- Renaming a table or column
- Changing a column's type
- Changing the semantics of an existing column
- Removing a value from a `source` enum

**Non-breaking (no bump):**

- Adding a new table
- Adding a new column with a sensible default
- Adding a new value to a `source` enum
- Adding a new field to `manifest.json`

The analyser refuses to load artifacts at a `schema_version` it doesn't
recognise. Pre-1.0, expect several bumps as the schema settles.

---

## Implementation constraints

- **Static binary.** `CGO_ENABLED=0`; pure-Go SQLite driver
  (`modernc.org/sqlite`); no native extensions on the client machine; no
  ML/NLP models. The single-static-binary, checksum-verifiable
  security-review story is the whole point of the Go choice.
- **Language classification.** `file_metrics.language` via go-enry (pure-Go
  Linguist port). Classification only; no content statistics.
- **Timestamps.** All UTC ISO-8601 strings. The `window` is interpreted in
  UTC.
- **Core/connector seam.** Core defines the canonical model, command
  surface, and connector registry. Core has no knowledge of any specific
  connector; connectors depend on core, never the reverse.
- **HTTP transport.** Every connector wraps its `Transport` with
  `&ratelimit.Transport{Base: ..., Policy: ratelimit.DefaultPolicy(), Log: log}`.
  For oauth2-wrapped clients, the wrap goes on `oauth2.Transport.Base` so
  retries see the token. `4xx` other than `429` is permanent failure; `429`
  and `5xx` retry with exponential backoff, capped at three attempts per
  request and 60 seconds cumulative wait.

---

## Workflow

- **Trunk-based on `main`.** No feature branches, no per-agent worktrees, no
  PR-driven merges. Commits land directly on `main`, gated by `/ready`.
- **Pathspec commits only.** `git commit -m '...' -- <paths…>` so concurrent
  sessions don't sweep each other's staged files. Enforced by `guard-commit.sh`.
- **Sequential work on shared paths.** When two pieces of work touch the same
  file, they run serially. Surface the conflict; do not parallelize.
- **Sessions claim disjoint file sets.** Orchestrators partition by file
  ownership before fan-out.
- **No worktrees.** Enforced by `guard-worktree.sh` and the `Bash` matcher
  on `git worktree add`.
- **Never defer bugs.** Any bug surfaced during `/ready` — in your own diff,
  adjacent to it, or pre-existing in code you read while reviewing — gets
  fixed in the same session before close. Filing a follow-up issue for a
  bug is **not allowed**. Only genuine scope additions (new features, new
  capabilities, design questions that need user input) become new issues.
  The test: *missing correctness, or a new thing?* If correctness, fix now.
- **Same-class scan before fixing.** Articulate the bug's abstract shape;
  grep for siblings; the class is the unit of work, not the instance.
  Peers found in scope get fixed in the same PR; peers out of scope become
  a single class-level issue, never N instance-level ones. Done at plan
  time (informs the fix) and again at `/ready` Step 4 (catches drift).
  See [`.claude/commands/class-scan.md`](.claude/commands/class-scan.md) for
  the standalone version.

---

## Planning escalation

Default model: Sonnet. Before implementing any fix that meets ANY of these
triggers, invoke `Agent({subagent_type: "Plan", model: "opus", ...})` and
consume the returned plan before writing code:

- Same-class scan returns >2 instances spanning >1 file.
- Fix touches both `internal/connectors/X` and either `internal/model/` or
  a `store/` DDL.
- Issue lists ≥2 viable approaches with real trade-offs.
- Spec/code drift fix where either direction is plausible.
- New connector skeleton, new schema column, or new manifest field.

Everything else — single-file edits, doc tweaks, mechanical fixes, gates,
`/commit`, `/release`, `/publish-tap` — stays on the Sonnet main loop.

Subagents inherit the main-loop model. `model: "opus"` is mandatory in
the trigger, not optional. Same applies to `agent()` opts in `Workflow`
scripts.

`/ready` Step 3 invokes `/code-review`; at `high` / `max` / `ultra`
effort that runs on Opus already — leave it.

---

## Gates

- `make gates` — `lint` + `govulncheck` + `coverage` (the three CI gates
  locally)
- `bin/ship` — pre-push wrapper for `make gates`
- `/ready` — full completion gate (gates → deterministic review →
  inferential review → scope sweep → docs → **smoke** → handoff)

**Closing an issue requires `/ready`.** `make gates` green + `git push`
are necessary but not sufficient: the smoke step in `/ready` catches
regressions that unit tests miss (perf wins that didn't materialise,
schema changes that pass DDL checks but break extraction at scale,
behaviour shifts in row volume). `/start` Step 11 enforces this — no
`gh issue close` until `/ready` reports clean.

Lint thresholds (`.golangci.yml`) and coverage thresholds
(`.testcoverage.yml`) are permissive at v0.x: set above the current
baseline so regressions are caught while the connector surface stabilises.
Tighten as functions are refactored.
