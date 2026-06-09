# xray CLI specification

`xray` is a read-only extractor that produces a portable, inspectable metrics
artifact from a client's engineering systems (git history, GitHub PRs and
reviews, CI builds, error tracker, observability). The artifact is consumed
downstream by analysis tooling. The CLI runs entirely in the client's
environment; nothing leaves that environment except the produced artifact
when the client chooses to send it.

This document is the product spec — what the tool does, how it's configured,
what it produces. The agent-facing constraint sheet (settled context, doors
closed, invariants, non-goals, schema versioning rules) lives in
[`CLAUDE.md`](../CLAUDE.md). The schema reference lives in
[`schema.md`](schema.md).

---

## Commands

All commands accept `--help` and emit standard `--version`.

### `xray init`

Generates a starter config file. Discovers repos via the GitHub API, emits a
runnable TOML scaffold with all repos under a single `unassigned` team and
connector blocks ready to be filled in.

```
xray init --org <github-org> [--out xray.toml] [--token <token>]
```

Token sourcing for `init` only: `--token` flag, else `GITHUB_TOKEN` env var.
This is the one place env is involved, because the config file does not yet
exist. After `init`, the client pastes the token into the generated file
alongside the others.

Output: writes TOML to `--out` (default `xray.toml`). Refuses to overwrite an
existing file unless `--force`.

### `xray validate`

Offline syntactic and schema check on a config file. No network. Exits 0 on
valid, non-zero with line-referenced diagnostics on invalid.

```
xray validate <config>
```

### `xray check`

Live preflight. Runs `validate` first; on success, verifies `git` is on
PATH, performs a read-only authentication ping against each configured
connector, and verifies clone access for each repo (a `git ls-remote`
suffices, no actual clone). Reports per-connector and per-repo status.
Exits 0 iff everything passes.

```
xray check <config>
```

Sample output:

```
ok  config valid
ok  github           authenticated (read-only)
ok  github_actions   authenticated (read-only, via github token)
ok  circleci         authenticated
ok  sentry           authenticated (read-only)
FAIL bugsnag          401 — token rejected
ok  honeycomb        authenticated
ok  kmcd/foo         clone access ok
ok  kmcd/bar         clone access ok
```

### `xray run`

Full extraction. Runs `check` first; on success, clones repos, extracts data
from each configured source within the configured window, populates the
canonical data model in SQLite, writes the manifest, and produces a
timestamped `.tar.gz`.

```
xray run <config> [--out <path>] [--workers N] [--keep-clones]
                  [--output auto|quiet|json|log] [--quiet] [--verbose]
```

- `--out` default: `./xray-export-<UTC-timestamp>.tar.gz`
- `--workers` default: 4. Bound for parallel clone/extract.
- `--keep-clones`: skip cleanup of temp clones (debugging).
- `--output` selects the run-time output mode (see "Output modes" below).
  `--quiet` is a shorthand for `--output quiet`; combining the two with
  conflicting values is a flag-level error.
- `--verbose` is orthogonal to `--output` and lowers the stderr log level
  to Debug (per-API-call timing). It does not alter the `json` event schema.

#### Output modes

| value | behaviour |
|---|---|
| `auto` (default) | TTY → status grid + summary block; non-TTY → line-based stderr log. |
| `quiet` | Errors to stderr only; on success, the artifact path is the only stdout line. |
| `json` | One NDJSON object per progress event to stdout, terminated by a `{"kind":"summary",...}` object. |
| `log` | Force the line-based stderr log even on a TTY (today's behaviour). |

The TTY status grid (`auto`), rate-limit events (`json` / `log`), and
TTY post-run summary block (`auto`) are owned by the cli-ux cluster
follow-ups (#81, #82, #84); the `--output` enum is the coordination
point and is stable.

#### Exit codes

| code | meaning |
|---|---|
| 0 | Clean run; artifact produced; all connectors green. |
| 1 | Config or pre-flight error; no artifact produced. |
| 2 | Partial run; artifact produced; one or more connectors failed for one or more repos (failure is recorded in the manifest). |
| 3 | Fatal error; artifact may or may not exist; investigate the run log. |
| 130 | Run interrupted by `SIGINT` / `SIGTERM`; no artifact produced. See [Cancellation](#cancellation). |

`xray validate` and `xray check` use only exit codes 0 and 1.

#### Cancellation

`xray run` traps `SIGINT` (Ctrl-C) and `SIGTERM` and shuts down
cooperatively:

- **First signal** → in-flight goroutines drain to the nearest
  checkpoint, the per-run temp directory `/tmp/xray-<run-id>-<rand>` is
  removed (preserved with `--keep-clones`), and a summary block is
  printed to stderr regardless of `--output` mode:

  ```
  Interrupted at phase 'extract' (kmcd/foo:github, kmcd/bar:github in flight).
  Cleaned up temp directory /tmp/xray-…-abc.
  No artifact produced. Re-run from scratch to retry; runs are non-incremental.
  Exit code: 130.
  ```

  Phase is one of `clone`, `extract`, `postprocess`. The in-flight list
  is included for the `extract` phase only.

- **Second signal** (at any time during graceful drain) → immediate
  `exit(130)` skipping all cleanup. The temp directory is named in a
  `force exit; temp dir <path> not cleaned` line on stderr so the
  operator can remove it manually. Use this when graceful drain is
  taking too long against a slow API.

No artifact is produced for either path. Runs are
non-incremental, so resuming after Ctrl-C means re-running from
scratch.

#### JSON event schema

`--output json` writes one NDJSON object per event to stdout. The final
event is always `{"kind":"summary",...}`:

```json
{"ts":"2026-06-09T14:10:23Z","kind":"phase_start","repo":"kmcd/foo","connector":"github","phase":"prs"}
{"ts":"2026-06-09T14:10:45Z","kind":"phase_progress","repo":"kmcd/foo","connector":"github","phase":"prs","done":47,"total":120}
{"ts":"2026-06-09T14:11:02Z","kind":"summary","artifact":"/abs/path/xray-export-…tar.gz","sha256":"…","rows":{"prs":120},"exit_code":0}
```

The JSON event schema is **versioned independently of the artifact
`schema_version`** — it is not part of the analyser contract that
`schema_version` carries. Additive changes are non-breaking and do not
bump any version:

- adding a new `kind` value
- adding a new optional field to an existing kind

Renames or removals bump a separate `output_schema_version` integer
(introduced when the first such change ships; absent today). The
artifact `schema_version` is unaffected by changes to the JSON event
schema.

### `xray version`

Prints version and exits.

---

## Config file

TOML, per-engagement, hand-edited by the client.

```toml
# generated by xray init — review and edit before running

window = "2025-01-01..2025-06-30"

# Optional. When true, capture the content of harness/AI-tool config files
# (CLAUDE.md, .cursor/rules, .github/copilot-instructions.md, etc.) in
# addition to their metadata and git timeline. Default false. Enabling this
# ships the rules content in the artifact and weakens the no-source-content
# guarantee for those specific paths only.
capture_harness_content = false

[teams]
unassigned = ["kmcd/foo", "kmcd/bar", "kmcd/baz"]

[connectors.github]
token = "ghp_..."

[connectors.github_actions]
# inherits token from [connectors.github] by default;
# set `token = "..."` here to override with a separate PAT.

[connectors.circleci]
token = "..."

[connectors.sentry]
token = "..."
organization = "my-org"
# map sentry project slugs to repo slugs so incidents tag to the right repo/team
[connectors.sentry.projects]
"api-backend" = "kmcd/foo"
"web-frontend" = "kmcd/bar"

[connectors.bugsnag]
token = "..."
# map bugsnag project slugs to repo slugs so incidents tag to the right repo/team
[connectors.bugsnag.projects]
"foo-api" = "kmcd/foo"
"bar-web" = "kmcd/bar"

[connectors.honeycomb]
token = "..."
dataset = "production"
```

### Schema rules

- `window` is required. Format: `YYYY-MM-DD..YYYY-MM-DD`. Inclusive on both
  ends. Must be a valid date range; end >= start.
- `[teams]` is required and must contain at least one team with at least one
  repo. Team names are arbitrary strings, no whitespace. Repos are
  `owner/repo` slugs.
- A repo may appear in only one team.
- `[connectors.X]` blocks are all optional. If absent, that source is skipped
  in extraction. If present, `token` is required.
- `bugsnag.projects` is required when bugsnag is configured; maps bugsnag
  project slug -> repo slug. Repos not in the map produce no bugsnag data.
- `sentry.organization` and `sentry.projects` are required when sentry is
  configured. `sentry.projects` maps sentry project slug -> repo slug.
- `github_actions` is optional. If present, inherits `token` from
  `[connectors.github]` unless its own `token` is set. Requires
  `[connectors.github]` to be configured.
- `honeycomb.dataset` is required when honeycomb is configured.
- `capture_harness_content` is optional, default `false`. When `true`,
  `harness_artifacts` rows include captured file content; when `false` or
  omitted, only metadata and git timeline are captured.

### Validation diagnostics

`validate` reports problems with line numbers from the source TOML, e.g.

```
xray.toml:7: window: end date precedes start date
xray.toml:12: teams.platform: repo "kmcd/foo" already appears in team "payments"
xray.toml:18: connectors.bugsnag: missing required key "projects"
```

---

## Output artifact

Single file: `xray-export-<UTC-timestamp>.tar.gz`.

Two entries:

### `manifest.json`

Extraction metadata. Auditable record of what was extracted, including
provenance for every repo x connector combination.

```json
{
  "tool_version": "0.2.0",
  "schema_version": 2,
  "run_id": "01J...",
  "run_started_at": "2026-06-04T10:14:22Z",
  "run_completed_at": "2026-06-04T10:32:51Z",
  "window": {"start": "2025-01-01", "end": "2025-06-30"},
  "teams": {
    "platform": ["kmcd/foo", "kmcd/bar"],
    "payments": ["kmcd/baz"]
  },
  "repos": [
    {"slug": "kmcd/foo", "head_sha": "abc123...", "default_branch": "main"},
    {"slug": "kmcd/bar", "head_sha": "def456...", "default_branch": "main"},
    {"slug": "kmcd/baz", "head_sha": "789aaa...", "default_branch": "main"}
  ],
  "connectors_used": ["github", "github_actions", "circleci", "sentry", "bugsnag", "honeycomb"],
  "counts": {
    "commits": 4218, "commit_files": 31044,
    "prs": 612, "reviews": 1144, "pr_comments": 3210,
    "builds": 8901, "build_jobs": 22730,
    "deploys": 233, "incidents": 47, "defects": 312,
    "file_metrics": 14620, "harness_artifacts": 6
  },
  "extraction_provenance": [
    {
      "repo": "kmcd/foo",
      "connector": "github",
      "window_covered": {"start": "2025-01-01", "end": "2025-06-30"},
      "rows_returned": {"commits": 412, "prs": 87, "reviews": 156},
      "pagination_complete": true,
      "rate_limit_truncated": false,
      "errors": {},
      "endpoints": {
        "branch_protection": {"accessible": false, "reason": "token lacks admin permission on repo"},
        "pr_review_requests": {"accessible": true},
        "codeowners": {"accessible": true}
      }
    },
    {
      "repo": "kmcd/foo",
      "connector": "sentry",
      "rows_returned": {"incidents": 12},
      "pagination_complete": true,
      "rate_limit_truncated": false,
      "errors": {}
    }
  ]
}
```

No tokens, no secrets, ever, in the manifest.

The `extraction_provenance` block records what each connector actually
returned per repo, enabling the analyser to distinguish "absent because
nothing happened" from "absent because we couldn't read it." Every
permission-gated endpoint (branch protection, audit log, organisation
membership, etc.) carries an `accessible` flag and a `reason` when not.
Absence-because-inaccessible is interpreted as **unknown** rather than
**no signal** — a critical distinction for any analysis that depends on
the data being there to mean something.

### `metrics.sqlite`

The canonical data model. Tables are grouped here by domain for readability;
the database itself is flat.

**Repo and organisational structure:**

- `repos(slug, default_branch, head_sha, team, primary_language, created_at, is_fork, is_archived, visibility, contributor_count, commits_in_window, prs_in_window, commits_all_time, prs_all_time)`
- `teams(name, repo)`
- `repo_languages(repo, language, bytes)`
- `branches(repo, name, last_commit_sha, last_commit_at, is_default)`
- `branch_protection(repo, branch, required_reviews, required_checks, enforce_admins, restricts_pushes)`
  — Populated only where the token has admin permission. When inaccessible,
  rows are absent and the manifest's `extraction_provenance` records this.
- `codeowners(repo, pattern, owner_handle, owner_type)`
  — `owner_type`: `user` or `team`. Parsed from the repo's CODEOWNERS file.

**Code history:**

- `commits(sha, repo, author_handle, committer_handle, authored_at, committed_at, additions, deletions, files_changed, message_subject, author_is_bot, committer_is_bot, signature_verified, landed_via_pr, reverts_sha, is_revert, is_merge, has_hotfix_marker)`
  — `committer_handle` is distinct from `author_handle`; tools, web UI, and
  bots commit under separate identities. `landed_via_pr` distinguishes
  PR-merged commits from direct pushes. `reverts_sha` is parsed from the
  body's `This reverts commit <sha>` line. `signature_verified` is nullable
  when not applicable. The full message body is parsed at extract for these
  signals and discarded.
- `commit_files(commit_sha, repo, path, additions, deletions, change_type, prev_path)`
  — Per-file numstat. `change_type`: `A` / `M` / `D` / `R` / `C`. `prev_path`
  populated for renames; required for hotspot history continuity across
  renames. Source: `git log --numstat --name-status`. Counts only; no
  patch text.
- `commit_coauthors(commit_sha, repo, handle, source, kind)`
  — `source`: `trailer` (parsed from `Co-authored-by:` body line) or `api`
  (GitHub co-author attribution). `kind`: `human`, `bot`, or `ai_tool` when
  identifiable from handle / email pattern (e.g. `noreply@anthropic.com`,
  Cursor / Copilot patterns). Heuristic; treated as best-effort signal.

**Pull requests:**

- `prs(number, repo, title, opened_at, merged_at, closed_at, author_handle, additions, deletions, files_changed, base_branch, head_sha, merge_sha, merge_method, is_draft, ready_for_review_at, first_review_at, commit_count, head_repo, force_pushed_after_review, body_length, template_match, checklist_total, checklist_checked, has_risk_marker, code_block_count, image_count, link_count, issue_refs_count)`
  — `merge_method`: `merge` / `squash` / `rebase`. Squash collapses commit
  history at merge time and is load-bearing context for any commit-level
  analysis. `template_match` is a 0-1 conformance score against
  `.github/PULL_REQUEST_TEMPLATE.md` section presence when a template exists;
  null when no template. `has_risk_marker` is set when keywords appear in
  the body (`hotfix`, `urgent`, `wip`, `untested`, `temporary`, `hack`,
  `todo`, `fixme`, etc.). `force_pushed_after_review` is true if a force-push
  occurred after the first review submission, detected from the PR
  timeline — review-dismissing churn.
- `pr_commits(pr_number, repo, sha)`
  — The PR's commit list as returned by the PR commits API. Pre-squash
  commits are stored in both `commits` and `pr_commits` regardless of
  `merge_method`, so commit-level analysis remains valid for squashed PRs.
- `reviews(pr_number, repo, reviewer_handle, submitted_at, state, body_length)`
- `pr_comments(pr_number, repo, author_handle, author_is_bot, created_at, kind, body_length, in_reply_to, path)`
  — `kind`: `issue_comment` or `review_comment`. `path` populated for inline
  review comments; enables review-concentrated-on-hotspot correlation.
  `author_is_bot` catches AI review bots (CodeRabbit, Copilot Review,
  internal architecture-review agents). Bodies not stored; length only.
- `pr_review_requests(pr_number, repo, requested_handle, requested_type, requested_at)`
- `pr_labels(pr_number, repo, label)`

**CI / CD:**

- `builds(id, repo, source, pipeline, status, conclusion, started_at, completed_at, duration_seconds, commit_sha, branch, event, attempt, rerun_of_id, created_at, pr_number)`
  — `source` distinguishes connectors that populate `builds` (`circleci`,
  `github_actions`). `event`: `push` / `pull_request` / `schedule` /
  `manual`. `conclusion` is finer than `status`: `success` / `failure` /
  `cancelled` / `timed_out` / `skipped` / `neutral`. `attempt` and
  `rerun_of_id` capture same-SHA fail-then-pass-on-rerun, which is the
  flaky-test signal without test-artifact parsing. `created_at` is queue
  entry; `started_at - created_at` is queue delay.
- `build_jobs(build_id, repo, name, status, conclusion, duration_seconds, attempt)`
  — Per-job rows for parallel CI. Enables job-level bottleneck analysis.
- `deploys(id, repo, environment, deployed_at, commit_sha, source, status, supersedes_deploy_id, rolled_back, trigger, release_tag, version)`
  — `source`: `github` for releases, `honeycomb` for markers,
  `github_actions` for Deployments API, etc. `status`: `success` / `failed`
  / `rolled_back` / `in_progress`. `supersedes_deploy_id` is a foreign key
  to the prior deploy in the same env that this one rolls back;
  `rolled_back` is set true on the deploy *being* superseded by a rollback.
  This linkage enables proper change-failure-rate measurement.
- `releases(repo, tag, name, created_at, sha, is_prerelease)`

**Quality signals:**

- `incidents(id, repo, source, opened_at, resolved_at, severity, occurrences, release_ref, deploy_id, commit_sha, acknowledged_at, is_regression, culprit_ref)`
  — `occurrences` is per-incident event volume. `release_ref` is the release
  identifier the error tracker attributes to the incident. `deploy_id` and
  `commit_sha` are foreign keys when resolvable from `release_ref`.
  `acknowledged_at` enables MTTR decomposition (detect -> ack -> resolve).
  `culprit_ref` is the file/module/component the error tracker itself
  attributes (Sentry's own telemetry attribution; null where the source's
  native shape does not cleanly map — e.g. Bugsnag's top stack frame is not
  an exact equivalent and is emitted as null rather than synthesised).
- `defects(id, repo, ticket_ref, source, opened_at, closed_at)`
  — Populated by parsing ticket references from PR titles, PR bodies, and
  commit messages. No ticket-system integration required. Patterns matched:
    - `<PREFIX>-<N>` — uppercase prefix of two or more characters (first
      must be a letter, rest letters or digits), hyphen, positive integer.
      Matches Jira-style (`PROJ-123`), Linear (`ENG-4567`), Shortcut
      (`SC-89`).
    - `#<N>` — hash followed by positive integer. Treated as a GitHub-style
      reference to the same repo's issue tracker.

  `source` records where the reference was found: `pr_title`, `pr_body`, or
  `commit_message`. `opened_at` and `closed_at` are derived from the
  containing PR (open and merge times respectively); commit-only references
  use commit time as `opened_at` and leave `closed_at` null.

**Source state:**

- `file_metrics(repo, path, snapshot_sha, language, is_binary, is_generated, is_vendored, is_test, is_dependency_manifest, size_bytes, loc_total, loc_code, loc_blank, max_indent, mean_indent, max_line_length, p95_line_length)`
  — Snapshot at `repos.head_sha`. `language` via go-enry (pure-Go port of
  GitHub Linguist). `is_generated` and `is_vendored` follow linguist
  heuristics and `.gitattributes`. `is_test` matches path patterns
  (`*_test.*`, `spec/`, `__tests__/`, `*.test.*`, `*.spec.*`).
  `is_dependency_manifest` matches names (`Gemfile`, `package.json`,
  `go.mod`, etc.). `max_indent` is the Tornhill-style complexity proxy
  enabling change-frequency x complexity hotspot analysis without parsing.
  `p95_line_length` flags generated / minified content linguist may
  misclassify. No content, no AST, no per-language semantic analysis.
- `harness_artifacts(repo, path, tool, kind, line_count, first_seen_commit, first_seen_at, last_modified_at)`
  — Inventory of AI-tool config artifacts in the repo, with adoption
  timeline derived from the git log of each path. `tool`: `claude_code` /
  `cursor` / `copilot` / `aider` / `windsurf` / `continue` / `generic_mcp`
  / `unknown`. `kind`: `rules` / `instructions` / `workflow` / `mcp` /
  `skills` / `agents` / `commands`. Paths scanned include `CLAUDE.md`,
  `.claude/**`, `AGENTS.md`, `.cursor/rules`, `.cursorrules`,
  `.github/copilot-instructions.md`, `.aider*`, `aider.conf.yml`,
  `.windsurfrules`, `.continue/**`, `.mcp.json`, and `.github/workflows/*`
  files invoking AI bots. Content is captured only when
  `capture_harness_content = true` in the config; default is metadata and
  timeline only.

All tables team-tagged where applicable via the `repo` foreign key. No
developer-identifier columns beyond opaque `*_handle` strings used solely
for linkage; no per-individual aggregation tables.

---

## Connectors (v1)

Each connector exposes a stable name, a read-only authentication check
(used by `xray check`), and an extract step that pulls data for a repo
within the configured window and emits canonical rows. Connectors must
never write to remote systems and must support clean cancellation. Adding
or removing a connector does not change the CLI surface.

The "emit null where the source's native shape does not cleanly map" rule
is general: connectors do not synthesise data to fill canonical columns.
Absence is recorded in `extraction_provenance`; the analyser interprets
"absent-because-inaccessible" or "absent-because-not-supported" as
*unknown*, not *no signal*.

- **github** — populates `commits`, `commit_files` (via git numstat),
  `commit_coauthors` (trailers + GitHub API), `prs`, `pr_commits`,
  `reviews`, `pr_comments`, `pr_review_requests`, `pr_labels`, `codeowners`,
  `branches`, `branch_protection` (where accessible), `releases`,
  `repo_languages`, `harness_artifacts` (working-tree walk + per-path git
  log; content only when `capture_harness_content = true`), `file_metrics`
  (working-tree walk at `head_sha`), and `deploys` (from releases and the
  Deployments API). Uses git protocol for clone + the GitHub REST/GraphQL
  APIs.
- **github_actions** — populates `builds`, `build_jobs`, and `deploys`
  (workflows that use the Deployments API). Shares the github connector's
  token by default; same API host, no extra credential to provision.
- **circleci** — populates `builds` and `build_jobs`. Project discovery via
  the configured token's accessible projects, filtered to repos in config.
- **sentry** — populates `incidents`. Uses `[connectors.sentry.projects]`
  to map sentry projects to repos. Largest install base of any error
  tracker; the connector most prospects will already have in place.
- **bugsnag** — populates `incidents`. Uses `[connectors.bugsnag.projects]`
  to map bugsnag projects to repos. `culprit_ref` emitted as null where
  Bugsnag's stack-frame shape does not cleanly map.
- **honeycomb** — populates `deploys` (deploy markers, with `trigger` and
  `release_tag` where available). May augment `incidents` via SLO burn
  events.

Ticket references parsed from PR/commit text populate `defects` and require
no connector.

---

## Behaviour

- **Cloning.** Shells out to the system `git` binary; relies on the user's
  ambient git authentication (SSH keys, credential helper, gh CLI, etc.).
  `xray` does not manage credentials for clone — if the user can `git clone`
  a repo from their terminal, `xray` can. The GitHub token in the config is
  used for API access only, not for clone. Clones land in a per-run temp
  directory (e.g. `/tmp/xray-<run_id>/`), deleted on completion unless
  `--keep-clones`. Full history within the configured window is required for
  `commit_files` rename tracking; use `--shallow-since=<window.start - 30d>`
  or a full clone as appropriate.
- **Concurrency.** `--workers N` bounds parallel clone + extract. Connector
  API rate limits are respected per connector regardless of worker count.
- **Idempotence.** Each run is fully independent. No incremental state, no
  caching across runs in v1.
- **Read-only.** No connector ever writes. No PR comments, no labels, no
  webhooks, no installations. If a token is granted write scope, the tool
  still issues only read calls; this is asserted in `check` output where
  the provider exposes scope.
- **Failure model.** A failed connector for one repo does not halt the run.
  Per-repo, per-connector status is recorded in `manifest.json` under the
  `extraction_provenance` block; the run still produces an artifact and
  exits non-zero if any source failed.
- **Permission-gated endpoints.** Endpoints requiring elevated scope
  (branch protection, audit logs, organisation membership, etc.) are
  reported per-repo in `extraction_provenance` with `accessible` and a
  reason when not. Inaccessible endpoints produce no rows; the analyser
  reads this as *unknown*, not as *absent*.
- **Rate limits and retries.** Connectors respect provider-published rate
  limits via `X-RateLimit-*` response headers where available. On `429` or
  `5xx`, retry with exponential backoff and jitter, capped at three
  attempts per request and 60 seconds cumulative wait. `4xx` responses
  other than `429` are treated as permanent failure (no retry). Rate-limit
  waits are logged at info level so the client can see why a run is slow.
- **github PR enrichment.** `prListQuery` walks PRs at 50 per page ordered
  by `UPDATED_AT DESC`, stopping when a node's `updatedAt` falls before
  `window.start`. Each PR node carries inline GraphQL connections that
  populate the per-PR child tables in a single round-trip: `reviews(first:
  100)` → `reviews`, `comments(first: 100)` → `pr_comments` (issue-comment
  kind), `reviewThreads(first: 100) { comments(first: 100) }` →
  `pr_comments` (review-comment kind, with `path` and `in_reply_to`), and
  a `timelineItems` projection that captures `REVIEW_REQUESTED_EVENT` →
  `pr_review_requests`. The `merge_method` classifier reads
  `mergeCommit.parents.totalCount` from the same query; the local-clone
  reachability check (`git merge-base --is-ancestor` against the per-run
  clone, per ADR 021) is unchanged. Bodies are length-measured at parse
  time and discarded — they never reach a column. Per-PR overflow
  paginators fire only when an inner connection's `pageInfo.HasNextPage`
  is true (more than 100 reviews, 100 top-level comments, 100 review
  threads, or 100 timeline items on a single PR); rare in practice and
  bounded by additional GraphQL calls per affected PR. Provenance is per
  row regardless of which path emitted it; a permission-gated endpoint
  unreadable inside the inline walk falls back to
  `EndpointStatus{Accessible: false}` exactly as the previous fan-out did.
- **PR-fetch / clone overlap.** Connectors may implement the optional
  `connector.Prefetcher` interface (`Prefetch(ctx, slug, window) error`),
  in which case `xray run`'s clone phase fires `Prefetch` as a goroutine
  alongside each repo's `git clone`. The github connector implements it
  by running the paginated `prListQuery` walk during the clone window
  and stashing the resulting `prGraph` nodes per slug on the connector;
  `Extract` consumes the cache when present and falls back to a live
  fetch on cache miss. The connector contract from ADR 022 is unchanged
  — `Extract(ctx, repo, window, sink) Provenance` is still the canonical
  entry point and the row-emit path. Provenance semantics are unchanged
  too: a new `(*Provenance).Merge` helper folds the two intra-Extract
  goroutine fragments (clone-bound stages vs PR stage) before returning
  the result.
- **Logging.** Logs go to stderr at info level by default; `--verbose` adds
  per-API-call timing; `--quiet` suppresses non-error output (shorthand for
  `--output quiet`). Tokens are never logged at any level. The full output
  mode and exit-code contract live under `xray run` above.

---

## Distribution

- **Releases**: GitHub Releases with SHA256 checksums per asset and a
  signed `checksums.txt`. The client's security review verifies the binary
  against the published checksum before running it. `xray version` embeds
  build commit and date via standard Go `-ldflags` injection.
- **Platforms**: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64;
  optionally windows/amd64. No runtime, no package manager, no
  native-extension build on the client machine.
- **Versioning**: `xray` itself follows semver. Binary version and schema
  version are decoupled — a single major version of the binary may emit
  multiple schema versions over its life, and `manifest.json` records
  both. Pre-1.0, minor bumps may introduce breaking changes; the changelog
  calls them out explicitly. A compatibility table in the README maps
  `xray` versions to the `schema_version` they emit.
