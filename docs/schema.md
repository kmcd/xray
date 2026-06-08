# xray metrics.sqlite schema (schema_version = 2)

Generated from `internal/model/schema.go`. The canonical row structs are in `internal/model/types.go`. This document mirrors the DDL applied at store-open time.

Every timestamp is stored as a UTC ISO-8601 (`RFC3339`) string. Booleans are stored as `INTEGER` (0/1). `NULL` means **unknown** — never *absent*. The `extraction_provenance` block in `manifest.json` records which endpoints were inaccessible so that `NULL` can be interpreted correctly.

## Author handles (`*_handle` columns)

Every `author_handle` / `committer_handle` / `reviewer_handle` / `commit_coauthors.handle` value is an **opaque token of the form `h_<15 digits>`** (regex `^h_\d{15}$`). The pre-image is one of two namespaces:

- **commit identities** — `Name <email>` after `.mailmap` canonicalisation, hashed via sha256, low 64 bits modulo 10^15.
- **GitHub logins** — lowercased login prefixed with `@`, hashed the same way.

The `@`-prefix on login canonicalisation keeps the two namespaces disjoint, so a login `alice` and a commit name `alice` (with no email) hash to distinct tokens. Without per-user email resolution there is no way to link them safely; fusing them silently would merge unrelated people.

`manifest.mailmap_applied` reports whether the run resolved aliases through a `.mailmap`. When `false`, downstream analysers should treat alias-derived metrics (truck factor, `main_author_share`, Conway's-law signals) as inflated and surface a Tornhill Ch 13 caveat.

`pr_review_requests.requested_handle` and `codeowners.owner_handle` carry the raw user/team identifier — these tables do not feed authorship analysis and the team-vs-user distinction is load-bearing.

## `_schema`

Single-row table written at store-open. The analyser must refuse to load artifacts whose `schema_version` it does not recognise.

| column           | type    | notes |
| ---------------- | ------- | ----- |
| `schema_version` | INTEGER | this document covers `2` |
| `tool_version`   | TEXT    | xray binary version that produced the artifact |
| `applied_at`     | TEXT    | UTC RFC3339 |

## Repo and organisational structure

### `repos`

| column              | type    | notes |
| ------------------- | ------- | ----- |
| `slug`              | TEXT    | PK; `owner/repo` |
| `default_branch`    | TEXT    | from the github API |
| `head_sha`          | TEXT    | HEAD of the clone at extract time |
| `team`              | TEXT    | from the config's `[teams]` block |
| `primary_language`  | TEXT    | go-enry detection over the working tree |
| `created_at`        | TEXT    | from the github API |
| `is_fork`           | INTEGER | |
| `is_archived`       | INTEGER | |
| `visibility`        | TEXT    | `public`, `private`, `internal` |
| `contributor_count` | INTEGER | distinct commit-author handles in the window |
| `commits_in_window` | INTEGER | |
| `prs_in_window`     | INTEGER | |
| `commits_all_time`  | INTEGER | best-effort; populated only if the API exposes the count cheaply |
| `prs_all_time`      | INTEGER | best-effort |

### `teams`

`PRIMARY KEY (name, repo)`. Flattened from `[teams]` for joinability.

### `repo_languages`

`PRIMARY KEY (repo, language)`. Bytes per detected language, from the GitHub languages API.

### `branches`

`PRIMARY KEY (repo, name)`. `last_commit_at` is committer time.

### `branch_protection`

`PRIMARY KEY (repo, branch)`. Populated only where the token has admin permission on the repo. When inaccessible, rows are absent and the manifest's `extraction_provenance.endpoints.branch_protection.accessible = false`.

`required_checks` is a comma-joined list of context names (CI checks). `required_reviews` is the approving-review count; `NULL` if no review-count rule is set.

### `codeowners`

One row per pattern + owner in the repo's CODEOWNERS file (parsed from `.github/CODEOWNERS`, `CODEOWNERS`, or `docs/CODEOWNERS`). `owner_type` is `user` or `team`.

## Code history

### `commits`

`PRIMARY KEY (sha, repo)`. Bodies are parsed at extract time and discarded.

| column               | type    | notes |
| -------------------- | ------- | ----- |
| `sha`                | TEXT    | |
| `repo`               | TEXT    | |
| `author_handle`      | TEXT    | resolved via the github commit-author API |
| `committer_handle`   | TEXT    | distinct from author for web-UI / tool commits |
| `authored_at`        | TEXT    | |
| `committed_at`       | TEXT    | |
| `additions`          | INTEGER | sum across files |
| `deletions`          | INTEGER | |
| `files_changed`      | INTEGER | |
| `message_subject`    | TEXT    | first line of the commit message |
| `author_is_bot`      | INTEGER | suffix `[bot]` or known bot accounts |
| `committer_is_bot`   | INTEGER | |
| `signature_verified` | INTEGER | nullable; `NULL` when not applicable / not fetched |
| `landed_via_pr`      | INTEGER | `1` if a PR **inside the extraction window** contained this SHA, `0` otherwise; derived in postprocess from a `(repo, sha)` join against `pr_commits` (#75). Window-restricted: a commit whose PR closed before `window.start` reads `0` here even though the commit historically landed via a PR. |
| `reverts_sha`        | TEXT    | from `This reverts commit <sha>` in body |
| `is_revert`          | INTEGER | subject begins with `Revert ` or revert-trailer present |
| `is_merge`           | INTEGER | parent count > 1 |
| `has_hotfix_marker`  | INTEGER | keyword scan of subject + body |

Indexed on `(repo, authored_at)`.

### `commit_files`

Per-file numstat from `git log --numstat --name-status`. **No patch text.**

| column        | type    | notes |
| ------------- | ------- | ----- |
| `commit_sha`  | TEXT    | |
| `repo`        | TEXT    | |
| `path`        | TEXT    | |
| `additions`   | INTEGER | |
| `deletions`   | INTEGER | |
| `change_type` | TEXT    | `A`, `M`, `D`, `R`, `C` |
| `prev_path`   | TEXT    | populated for `R` / `C` — required for rename continuity |

Indexed on `(repo, path)` and `(repo, commit_sha)`.

### `commit_coauthors`

`PRIMARY KEY (commit_sha, repo, handle)`.

- `source`: `trailer` (parsed from `Co-authored-by:`) or `api` (GitHub committer-author distinction).
- `kind`: `human`, `bot`, or `ai_tool` (heuristic match on handle / email pattern such as `noreply@anthropic.com`, Cursor / Copilot / Aider patterns).

## Pull requests

### `prs`

`PRIMARY KEY (repo, number)`.

| column                       | type    | notes |
| ---------------------------- | ------- | ----- |
| `number`                     | INTEGER | |
| `repo`                       | TEXT    | |
| `title`                      | TEXT    | |
| `opened_at`                  | TEXT    | |
| `merged_at`                  | TEXT    | nullable |
| `closed_at`                  | TEXT    | nullable |
| `author_handle`              | TEXT    | |
| `additions`                  | INTEGER | |
| `deletions`                  | INTEGER | |
| `files_changed`              | INTEGER | |
| `base_branch`                | TEXT    | |
| `head_sha`                   | TEXT    | |
| `merge_sha`                  | TEXT    | |
| `merge_method`               | TEXT    | `merge` / `squash` / `rebase` (heuristic from parent count when API doesn't expose it) |
| `is_draft`                   | INTEGER | |
| `ready_for_review_at`        | TEXT    | from the `ReadyForReview` timeline event |
| `first_review_at`            | TEXT    | minimum `submitted_at` over non-pending reviews |
| `commit_count`               | INTEGER | |
| `head_repo`                  | TEXT    | NameWithOwner of the head repo (cross-fork detection) |
| `force_pushed_after_review`  | INTEGER | timeline `head_ref_force_pushed_event` after first review |
| `body_length`                | INTEGER | bytes; body itself not stored |
| `template_match`             | REAL    | 0-1 score against `.github/PULL_REQUEST_TEMPLATE.md`; `NULL` if no template |
| `checklist_total`            | INTEGER | count of `- [ ]` plus `- [x]` |
| `checklist_checked`          | INTEGER | count of `- [x]` |
| `has_risk_marker`            | INTEGER | keyword scan |
| `code_block_count`           | INTEGER | triple-backtick fences |
| `image_count`                | INTEGER | `![alt](url)` occurrences |
| `link_count`                 | INTEGER | markdown links plus bare URLs |
| `issue_refs_count`           | INTEGER | count of `<PREFIX>-<N>` and `#<N>` refs in title + body |

### `pr_commits`

`PRIMARY KEY (repo, pr_number, sha)`. The PR's commit list as returned by the GraphQL `commits` connection. Pre-squash commits live in **both** `commits` and `pr_commits` regardless of `merge_method`, so commit-level analysis stays valid for squashed PRs.

### `reviews`

| column            | type    | notes |
| ----------------- | ------- | ----- |
| `pr_number`       | INTEGER | |
| `repo`            | TEXT    | |
| `reviewer_handle` | TEXT    | |
| `submitted_at`    | TEXT    | |
| `state`           | TEXT    | `APPROVED`, `CHANGES_REQUESTED`, `COMMENTED`, `DISMISSED` |
| `body_length`     | INTEGER | bytes |

Indexed on `(repo, pr_number)`.

### `pr_comments`

| column          | type    | notes |
| --------------- | ------- | ----- |
| `pr_number`     | INTEGER | |
| `repo`          | TEXT    | |
| `author_handle` | TEXT    | |
| `author_is_bot` | INTEGER | catches CodeRabbit, Copilot Review, internal architecture-review agents |
| `created_at`    | TEXT    | |
| `kind`          | TEXT    | `issue_comment` or `review_comment` |
| `body_length`   | INTEGER | |
| `in_reply_to`   | INTEGER | review comment thread parent; nullable |
| `path`          | TEXT    | populated for inline review comments — enables review-concentration-on-hotspot |

Indexed on `(repo, pr_number)`.

### `pr_review_requests`

History of review requests (request and removal). Populated from the timeline so both still-active and since-removed requests appear.

| column             | type | notes |
| ------------------ | ---- | ----- |
| `pr_number`        | INTEGER | |
| `repo`             | TEXT | |
| `requested_handle` | TEXT | |
| `requested_type`   | TEXT | `user` or `team` |
| `requested_at`     | TEXT | |

### `pr_labels`

`PRIMARY KEY (repo, pr_number, label)`.

## CI / CD

### `builds`

`PRIMARY KEY (repo, source, id)`. `source` is `github_actions` or `circleci`.

| column             | type    | notes |
| ------------------ | ------- | ----- |
| `id`               | TEXT    | provider-native ID |
| `repo`             | TEXT    | |
| `source`           | TEXT    | |
| `pipeline`         | TEXT    | workflow name / pipeline name |
| `status`           | TEXT    | provider-native (`completed`, `in_progress`, ...) |
| `conclusion`       | TEXT    | `success` / `failure` / `cancelled` / `timed_out` / `skipped` / `neutral` |
| `started_at`       | TEXT    | |
| `completed_at`     | TEXT    | |
| `duration_seconds` | INTEGER | nullable |
| `commit_sha`       | TEXT    | |
| `branch`           | TEXT    | |
| `event`            | TEXT    | `push`, `pull_request`, `schedule`, `manual` |
| `attempt`          | INTEGER | rerun number; >1 indicates retry |
| `rerun_of_id`      | TEXT    | for `attempt > 1` |
| `created_at`       | TEXT    | queue-entry time; `started_at - created_at` is queue delay |
| `pr_number`        | INTEGER | nullable |

Indexed on `(repo, commit_sha)`. Same-SHA fail-then-pass-on-rerun is the flaky-test signal — detect via `attempt > 1` and matching `commit_sha`.

### `build_jobs`

| column             | type    | notes |
| ------------------ | ------- | ----- |
| `build_id`         | TEXT    | |
| `repo`             | TEXT    | |
| `name`             | TEXT    | |
| `status`           | TEXT    | |
| `conclusion`       | TEXT    | |
| `duration_seconds` | INTEGER | nullable when timestamps incomplete |
| `attempt`          | INTEGER | |

Indexed on `(repo, build_id)`.

### `deploys`

`PRIMARY KEY (repo, source, id)`. `source` is `github`, `github_actions`, or `honeycomb`.

| column                  | type    | notes |
| ----------------------- | ------- | ----- |
| `id`                    | TEXT    | |
| `repo`                  | TEXT    | |
| `environment`           | TEXT    | |
| `deployed_at`           | TEXT    | |
| `commit_sha`            | TEXT    | |
| `source`                | TEXT    | |
| `status`                | TEXT    | `success` / `failed` / `rolled_back` / `in_progress` |
| `supersedes_deploy_id`  | TEXT    | the deploy this one rolls back to |
| `rolled_back`           | INTEGER | true on the deploy *being* superseded by a rollback |
| `trigger`               | TEXT    | |
| `release_tag`           | TEXT    | |
| `version`               | TEXT    | |

Indexed on `(repo, environment, deployed_at)`. Rollback heuristic: in chronological order per `(repo, environment)`, a deploy whose `commit_sha` matches `commit_sha[i-2]` and differs from `commit_sha[i-1]` is treated as a rollback. Documented in `internal/postprocess`.

**Honeycomb attribution caveat.** Honeycomb has no per-repo concept, so all `deploys` rows with `source = "honeycomb"` carry the alphabetically-first configured repo as their `repo` value. This is a v1 limitation; analysers should treat `(repo, source = "honeycomb")` as an approximate attribution. The same applies to `incidents` rows from SLO burn alerts.

### `releases`

`PRIMARY KEY (repo, tag)`. One row per GitHub Release.

## Quality signals

### `incidents`

`PRIMARY KEY (repo, source, id)`. `source` is `sentry`, `bugsnag`, or `honeycomb`.

| column            | type    | notes |
| ----------------- | ------- | ----- |
| `id`              | TEXT    | |
| `repo`            | TEXT    | |
| `source`          | TEXT    | |
| `opened_at`       | TEXT    | |
| `resolved_at`     | TEXT    | nullable |
| `severity`        | TEXT    | source-native; passthrough |
| `occurrences`     | INTEGER | per-incident event volume |
| `release_ref`     | TEXT    | error-tracker's release identifier |
| `deploy_id`       | TEXT    | resolved post-extraction from `release_ref` |
| `commit_sha`      | TEXT    | resolved post-extraction (deploy.commit_sha or release.sha) |
| `acknowledged_at` | TEXT    | nullable; populated where the source has the concept |
| `is_regression`   | INTEGER | heuristic per connector — documented in each |
| `culprit_ref`     | TEXT    | source-native attribution (file/module). `NULL` for Bugsnag per spec |

Indexed on `(repo, opened_at)`.

### `defects`

`PRIMARY KEY (repo, id)`. Populated by parsing ticket references from PR titles, PR bodies, and commit messages — no ticket-system integration in v1.

| column       | type | notes |
| ------------ | ---- | ----- |
| `id`         | TEXT | `repo:source:scope_id:ref` |
| `repo`       | TEXT | |
| `ticket_ref` | TEXT | `PROJ-123`, `ENG-4567`, `#123`, ... |
| `source`     | TEXT | `pr_title`, `pr_body`, or `commit_message` |
| `opened_at`  | TEXT | containing PR's opened_at or commit's committed_at |
| `closed_at`  | TEXT | containing PR's merged_at; `NULL` for commit refs |

Indexed on `(repo, ticket_ref)`.

## Source state

### `file_metrics`

Snapshot at `repos.head_sha`. **No content stored.** Walked at extract time.

| column                   | type    | notes |
| ------------------------ | ------- | ----- |
| `repo`                   | TEXT    | |
| `path`                   | TEXT    | |
| `snapshot_sha`           | TEXT    | == `repos.head_sha` |
| `language`               | TEXT    | go-enry classification |
| `is_binary`              | INTEGER | |
| `is_generated`           | INTEGER | linguist heuristics + `.gitattributes` |
| `is_vendored`            | INTEGER | linguist heuristics |
| `is_test`                | INTEGER | path patterns: `*_test.*`, `*.test.*`, `*.spec.*`, `/spec/`, `/__tests__/` |
| `is_dependency_manifest` | INTEGER | static filename allow-list (see ADR 008) |
| `size_bytes`             | INTEGER | |
| `loc_total`              | INTEGER | |
| `loc_code`               | INTEGER | non-blank lines |
| `loc_blank`              | INTEGER | |
| `max_indent`             | INTEGER | Tornhill-style complexity proxy |
| `mean_indent`            | REAL    | over non-blank lines |
| `max_line_length`        | INTEGER | bytes |
| `p95_line_length`        | INTEGER | flags generated / minified content |

`PRIMARY KEY (repo, path)`.

### `harness_artifacts`

`PRIMARY KEY (repo, path)`. Inventory of AI-tool config artifacts with adoption timeline.

| column              | type    | notes |
| ------------------- | ------- | ----- |
| `repo`              | TEXT    | |
| `path`              | TEXT    | |
| `tool`              | TEXT    | `claude_code` / `cursor` / `copilot` / `aider` / `windsurf` / `continue` / `generic_mcp` / `unknown` |
| `kind`              | TEXT    | `rules` / `instructions` / `workflow` / `mcp` / `skills` / `agents` / `commands` |
| `line_count`        | INTEGER | |
| `first_seen_commit` | TEXT    | from `git log` of the path |
| `first_seen_at`     | TEXT    | |
| `last_modified_at`  | TEXT    | |
| `content`           | TEXT    | populated only when `capture_harness_content = true` in the config |

### `file_complexity_history`

`PRIMARY KEY (commit_sha, repo, path)`. One row per touched, non-excluded file per commit in the window. Feeds assay's `stage2.flow.hotspot_complexity_trend` — the `indent_total` series for each hotspot file is fed through `framing.trend_classify` to label trajectories.

Indent measure is the Hindle/Godfrey/Holt 2008 logical-indent proxy: **4 spaces or 1 tab = 1 logical level** (integer division of raw spaces). This is intentionally different from `file_metrics.max_indent` / `mean_indent`, which count raw spaces — analysers must consult the right column for the right metric.

The exclusion regex (`internal/connectors/github/complexity_history.go::complexityHistoryExclusionRe`) drops `vendor/`, `node_modules/`, `__pycache__/`, `build/`, `dist/`, `.venv/`, dependency-lock files, generated files (`*.pb.go`, `_pb2.py`, `*.generated.*`, `*.min.js`), and common binary extensions. **Test files are NOT excluded** — assay computes the test/non-test split downstream.

| column         | type    | notes |
| -------------- | ------- | ----- |
| `commit_sha`   | TEXT    | |
| `repo`         | TEXT    | |
| `path`         | TEXT    | matches `commit_files.path` exactly so joins work |
| `n`            | INTEGER | count of lines with `indent_level > 0` |
| `indent_total` | INTEGER | sum of `indent_level` across those lines |
| `indent_mean`  | REAL    | `indent_total / n`; `0.0` when `n == 0` |
| `indent_sd`    | REAL    | sample stddev of per-line levels; `0.0` when `n < 2` |
| `indent_max`   | INTEGER | headline statistic per Tornhill 2nd ed Ch 5 |
