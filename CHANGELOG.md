# Changelog

All notable changes to `xray` per release. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [semver](https://semver.org/) on the binary, while `schema_version` (in `manifest.json` and the `_schema` SQLite table) is an integer bumped only on breaking changes to the output model.

The analyser refuses to load artifacts at an unknown `schema_version`. See the [compatibility table](./README.md#compatibility) in the README for the binary-to-schema mapping.

## [0.3.0] — 2026-06-08

Breaking: `schema_version` bumps `1 → 2`. Author-identity columns now hold opaque `h_<15 digits>` tokens, not raw logins or git idents. Analysers built for `schema_version = 1` must be updated (assay v1.1.0 already reads the new form). Smoke-verified against `goreleaser/chglog` (~12-month window): 36 commits + 36 PRs + 68 file_complexity_history rows; `mailmap_applied=false`, `squash_rate=1.0`, all author handles match `^h_\d{15}$`.

### Operational

- **run log file.** `xray run` now writes a `<artifact-stem>.log` sibling file alongside the `.tar.gz` by default, mirroring everything written to stderr. Useful for post-mortem inspection of runs on client machines where `2>&1 | tee` was not set up in advance. Opt out with `--no-run-log`. The file inherits the existing token-safety guarantee — the logging code never accepts credentials. ([#68])
- **run-time status display.** `xray run` on a TTY (`--output auto`, the default) now renders a live `(repo × connector)` grid showing each pair's state (`▢ pending`, `● running`, `✔ done`, `✘ error`, `🔒 inaccessible`), elapsed wall-clock, a per-connector ETA, and the most recent rate-limit / retry message. The redraw runs at ~5 Hz via hand-rolled ANSI cursor-up + clear-to-end; no new dependency. Non-TTY runs (CI, pipe to file) fall back to one stderr log line per phase boundary, matching today's content plus structured `phase=…`, `repo=…`, `connector=…` tags. `--output log` forces the log fallback even on a TTY. The `internal/progress.Sink` contract underpinning the grid is the load-bearing seam for `cli-ux` follow-ups (rate-limit visibility #82, post-run summary #84). ([#81])
- **rate-limit and retry visibility.** `ratelimit.Transport` now emits structured `progress.RateLimit` and `progress.Retry` events on every wait > 1 s and every retry attempt — the customer sees "rate limited, waiting 12 s" in the TTY grid (and in the JSON event stream) instead of an unexplained 12-second freeze. `Transport.Snapshot()` exposes the parsed `X-RateLimit-{Remaining,Limit,Reset}` budget per connector so the grid header can render "github: 4,213 / 5,000, resets in 28 m". A predictive `PhaseError` fires once per connector when remaining budget drops below 100 after 5+ minutes of runtime so the customer is warned before the multi-minute wait. Existing slog audit-trail output is unchanged. ([#82])
- **`check` pre-flight UX.** `xray check` now prints (1) the token's actual granted OAuth scopes per connector with a one-line "xray issues only read calls" assertion, surplus scopes called out without lecturing; (2) a Plan block with repo count, window days, estimated clone bytes, API calls, and wall-clock seconds — derived from cheap GraphQL aggregates (`diskUsage`, `pullRequests.totalCount`, windowed commit count); (3) an Inaccessible-endpoints list naming any per-repo permission-gated probe (`branch_protection` today) that the token cannot reach. `--no-cost-preview` skips block 2 for fast iteration. `xray check`, `xray validate`, and `xray init` now honour `--output {auto|quiet|json|log}`: quiet suppresses success lines, json emits one terminating `*_summary` object. ([#83])
- **post-run summary block.** `xray run` now prints a single-screen summary after the run completes: artifact path + size + sha256 + schema version + run-log path; row roll-up of the top eight tables by count with a `(N tables total)` tail; provenance counts (endpoints accessible / inaccessible, per-row errors, rate-limit-truncated connectors, partial paginations); a Partial block listing failed (repo, connector) combinations when the run exited non-zero; and a next-step reminder warning the customer not to ship the config file. `--output quiet` collapses to the bare artifact path; `--output json` emits a single `{"kind":"run_summary",…}` object. SHA256 is computed in the same `io.MultiWriter` pass that writes the .tar.gz — no second read of the artifact. ([#84])
- **graceful Ctrl-C.** `xray run` traps `SIGINT` and `SIGTERM`. First signal cancels the run cooperatively: in-flight goroutines drain to the nearest checkpoint via `ctx.Done()`, the per-run temp directory is removed (preserved with `--keep-clones`), and a stderr summary names the phase (`clone` / `extract` / `postprocess`) plus any in-flight `(repo, connector)` pairs. Exit code is 130 — POSIX convention for SIGINT termination, distinct from the existing 0/1/2/3 contract. Second signal at any time during graceful drain skips cleanup and immediately `exit(130)` with a `force exit; temp dir <path> not cleaned` line so the operator can remove it manually. The temp-dir path flows from `internal/run.Run` to the signal handler via a new `Options.OnTempDir` callback writing an `atomic.Pointer[string]` in `cmd/xray/main.go`. ([#86], [ADR 028](./docs/adr/0028-graceful-ctrl-c.md))

### Performance

- **github**: PR enrichment folded into the existing `prListQuery` as a single inline GraphQL walk — drops per-PR REST fan-out for `reviews`, `pr_comments`, `pr_review_requests`, and the merge-method parent-count lookup. Reviews, top-level comments, review threads (with nested comments), and `mergeCommit.parents.totalCount` are read inline at 25 items per inner connection; `timelineItems` adds `REVIEW_REQUESTED_EVENT` so review-request rows come from the same walk. Per-PR overflow paginators fire only when an inner `pageInfo.HasNextPage` is true. Baseline: a 30-day smoke against `posthog/posthog` on the pre-fix path took ~10 hours, dominated by 4–6 REST round-trips per PR; the inline path eliminates that fan-out entirely. Schema unchanged; `schema_version` stays at 2. ([#69])
- **github**: `file_complexity_history` content fetch collapsed from O(N) `git show` subprocess calls (one per touched file per commit) to a **single `git cat-file --batch`** subprocess. For a 2-year window on a large repo (posthog: ~500 k pairs @ ~35 ms each = hours of wall-clock) the process-start overhead dominated; the batch call eliminates it, leaving only disk I/O. New `gitcli.Client.CatFileBatch` drives the path; `ShowFile` is retained for single-object queries. ([#73])
- **github**: commit enrichment trimmed to `signature` only — `associatedPullRequests` removed from the alias-batched query. `enrichBatchSize` raised 25 → 100 (the 25-cap was forced by the heavy subquery's server-side timeouts) and `enrichBatchDelay` lowered 500 ms → 250 ms. `landed_via_pr` is now derived in postprocess from a `(repo, sha)` join against `pr_commits` and is window-restricted: a commit whose PR closed before `window.start` reports `false` here where the old global GraphQL form reported `true`. Column type and name unchanged; `schema_version` stays at 2. Expected drop on 50k-commit windows: ~23 min → ~100 s. ([#75])
- **github**: PR fetch now overlaps both the clone phase and the clone-bound extract stages. A new optional `connector.Prefetcher` interface lets `run.go` fire the paginated `prListQuery` walk as a goroutine alongside each repo's `git clone`; the results are stashed on the github `Connector` and consumed by `Extract`. Inside `Extract`, the clone-bound stages (languages / branches / codeowners / releases / commits / file_metrics / harness) and the API-bound PR stage now run as two goroutines, joined before the postlude. Provenance is collected as two fragments and folded via a new `(*Provenance).Merge` (first-wins on RowsReturned, AND on PaginationComplete). Estimated savings on `posthog/posthog` 7-day: ~150 s (~22%); larger windows save proportionally more clone time. Schema unchanged; `schema_version` stays at 2. ([#71])
- **github**: `prListQuery` nested connection sizes lowered from `first: 100` to `first: 25` for `commits`, `timelineItems`, `reviews`, `comments`, and `reviewThreads`. GitHub charges GraphQL points on the *requested* connection size, not the count returned; the previous values over-provisioned ~4× for typical PRs. Overflow paginators handle the rare PRs that exceed 25; `paginatePRTimelineOverflow` extended to request all four item types and update `ready_for_review_at`, `first_review_at`, and `force_pushed_after_review` so derived fields are correct even when those events fall past position 25. ([#77])
- **ratelimit**: proactive primary-limit pacing via `Policy.LowWaterMark` (default 200). After a response where `X-RateLimit-Remaining < LowWaterMark`, `ratelimit.Transport` sets an internal `paceUntil` timestamp (reset + 5 s) and sleeps at the *start of the next request* rather than after the current response. The deferred-sleep design means the goroutine that received the triggering response is never blocked — specifically, the prefetch goroutine can return normally so `cloneOneRepo` is not serialised behind a rate-limit window. ([#76])
- **github**: GraphQL point cost recorded in connector provenance. A `costInterceptor` wraps the outermost HTTP transport and parses `extensions.cost.actualQueryCost` and `extensions.cost.throttleStatus.remaining` from every `/graphql` response. Accumulated totals appear in `manifest.json` as `graphql_points_used` and `graphql_points_remaining` under `extraction_provenance`. Fields are additive, non-breaking, and omit when zero (classic PATs and non-GitHub tokens do not return extensions). ([#78])
- **github**: PR-list prefetch resilient to mid-walk EOF. `costInterceptor` now propagates body-read errors instead of swallowing them and re-attaching a partial body that the downstream JSON decoder would surface as `"unexpected EOF"`. `queryWithEOFRetry` (3 attempts, 60 s budget, 500 ms initial backoff) catches `io.ErrUnexpectedEOF`/`io.EOF`/decoder-surfaced "unexpected EOF" and resumes the same GraphQL cursor; applied to every `c.gql.Query` call site (`fetchPRs`, `paginatePRCommits`, `paginatePRReviewsOverflow`, `paginatePRIssueCommentsOverflow`, `paginatePRReviewThreadsOverflow`, `paginatePRTimelineOverflow`, `fetchBranchProtectionRules`). `enrich.go`'s raw commit-enrichment POST uses a sibling `doJSONPOSTWithEOFRetry`. When Prefetch exhausts its retry budget mid-walk, `prPrefetchResult` now stashes the failing page's cursor so `extractPRs` resumes the walk live from that cursor instead of dropping the unfetched tail. ([#80])

### Author alias resolution via `.mailmap` (assay v1.1 contract #20, Tornhill Ch 13)

- **`.mailmap` canonicalisation at extract time.** The repo's top-level `.mailmap` is parsed once per run into an in-memory resolution table; every commit (and Co-authored-by trailer) identity is rewritten to its canonical `Name <email>` before hashing. Pure-Go parser supports all four standard line shapes; smoke-tested against `git check-mailmap` in `internal/gitcli/mailmap_test.go`. The alias triple from the prompt (`Alice <alice@old>` / `Alice <alice@new>` / `Alice <alice@old>` with a `new → old` mapping) emits one canonical handle across all three commits.
- **Author handles hashed to opaque tokens.** `commits.author_handle`, `commits.committer_handle`, `commit_coauthors.handle`, `prs.author_handle`, `reviews.reviewer_handle`, `pr_comments.author_handle` switched from raw login/git ident to `h_<15 digits>` (low 64 bits of sha256 mod 10^15, zero-padded). The shape matches assay's `^h_\d{15}$` boundary check. See [ADR 023](./docs/adr/0023-author-handles-hashed-at-boundary.md) for the bump rationale.
- **`manifest.mailmap_applied` (new).** Bool aggregated across all repos in the run. `true` iff every repo carried a non-empty, cleanly parsed `.mailmap` that was applied to every author-handle table. `false` flips assay-side metrics like `knowledge_concentration` and `communication_paths` to surface the Tornhill alias caveat.

### Squash-merge detection rolled up to the manifest (assay v1.1 contract #21, Tornhill Ch 9)

- **`manifest.{n_squash_merged_prs, n_total_merged_prs, squash_rate}` (new).** Counts roll up post-extraction via `store.SquashStats()`, which queries `prs.merge_method = 'squash'` against `merged_at IS NOT NULL`. Per-PR classification is unchanged — still [ADR 021](./docs/adr/0021-merge-method-derivation.md)'s parent-count + PR-head reachability — only the aggregation is new. `squash_rate = 0.0` when no PRs merged in the window.
- assay treats `squash_rate > 0.5` as the Tornhill Ch 9 "Squash Sparingly" caveat threshold and attaches a coupling-derived-metric note. The threshold lives in `assay_evaluator/stage2/flow.py`; xray emits the raw rate.

### Per-revision indent stats (assay v1.1 contract #12, Tornhill Ch 5)

- **`file_complexity_history` table (new, additive).** One row per `(commit_sha, repo, path)` for every in-window commit's touched, non-excluded file. Columns: `n` (lines with `indent_level > 0`), `indent_total` (sum), `indent_mean`, `indent_sd` (sample stddev, `0.0` when `n < 2`), `indent_max`. Indent measure is the Hindle/Godfrey/Holt 2008 logical-indent proxy (4 spaces or 1 tab = 1 level) — intentionally distinct from `file_metrics.max_indent` / `mean_indent`, which count raw spaces. Feeds assay's `hotspot_complexity_trend` so trajectories like "rising indent on a hotspot file" can light up.
- **`internal/gitcli/Client.ShowFile` (new).** Streams `git show <sha>:<path>` with an 8 MiB output cap; surfaces `os.ErrNotExist` when the path doesn't exist at that revision. Used for single-object queries; `file_complexity_history` now uses `Client.CatFileBatch` instead (see Performance above).
- **Exclusion regex (`internal/connectors/github/complexity_history.go`).** Mirrors assay's `_NONTEST_EXCLUDED_PATH_RE`: `vendor/`, `node_modules/`, `__pycache__/`, `build/`, `dist/`, `.venv/`, dependency-lock files, `*.pb.go`, `_pb2.py`, `*.generated.*`, `*.min.js`, and common binary extensions. Test files are kept — assay computes the test/non-test split downstream.

## [0.2.2] — 2026-06-06

Performance + observability pass. No schema change; `schema_version` stays at 1. Validated against `goreleaser/chglog` post-fix: 65 commits with full enrichment (`signature_verified` + `landed_via_pr` populated on every row), 2:20 wall time.

### Performance

- **Batched GraphQL commit enrichment.** Per-commit REST calls for `signature_verified` and `landed_via_pr` previously ran serially — 3000 round-trips for 1500 commits, ~36 min at the authenticated rate-limit floor. Now batched into ~25-alias GraphQL queries via `internal/connectors/github/enrich.go`. PostHog-scale enrichment dropped from "never finished" to ~30 s of API time. ([#64])
- **Batch size pinned to 25 (was 100).** GitHub's GraphQL backend 502s on 100-alias queries with `associatedPullRequests` connections — the query exceeds the ~10 s server-side timeout. 25 aliases keeps each query under the timeout while still cutting per-commit REST calls by 25×.
- **Per-error-class retry budgets in `ratelimit.Transport`.** `Policy` now carries separate `CumulativeBudget` (60 s, for 429 + 5xx) and `SecondaryRateLimitBudget` (600 s, for GitHub anti-burst 403s). A long secondary-RL wait no longer starves the transient-error budget. ([#65])
- **Secondary-rate-limit detection.** Transport recognises 403 responses whose body contains `secondary rate limit`, `abuse detection`, or `exceeded a rate limit`; treats them as transient and retries with a documented-minimum 60 s wait per GitHub's docs. Body is peeked once and re-attached so the terminal caller still sees the full error envelope.

### Observability

- **Progress logging in the github connector.** `commits`, `prs`, and `file_metrics` stages emit `github: progress` checkpoints every 100 records or 30 s — whichever first. The "is it stuck?" black-box problem during long extracts is now answerable from the run log alone. ([#62])

## [0.2.1] — 2026-06-06

Five bug fixes surfaced by the v0.2.0 smoke test against `goreleaser/chglog`. No schema change; `schema_version` stays at 1.

Verified against `goreleaser/chglog` post-fix (~18-month window): 65 commits with non-zero numstat, 64 PRs with shape signals, 6 releases each with their own tag-resolved commit SHA, `tool_version` populated, no slug diagnostic on `<org>/.github`.

### Fixes

- **`gitcli` numstat preserved.** `git log --numstat --name-status` silently dropped numstat output on modern git (`--name-status` wins). Every `commits.additions` / `commits.deletions` / `commit_files.additions` / `commit_files.deletions` was 0 — hotspot and change-size analysis was impossible. Parser switched to `--numstat --raw`, which compose. Regression guard added in `internal/gitcli/gitcli_test.go`. ([#55])
- **`releases` filtered by window.** `extractReleases` shipped every release in the repo's history regardless of the configured window — the smoke run produced 19 releases dating back to 2020. Now skipped per `window.Contains` with early-stop paging on the created-at-desc ordering. ([#56])
- **`releases` / `deploys` get the tagged commit, not HEAD.** `resolveReleaseSHA` resolved `r.TargetCommitish` (typically a branch like `main`), so `GetCommitSHA1` returned the branch HEAD and every release on the same default branch stamped the same SHA. Now resolves the tag itself; falls back to `TargetCommitish` only when the tag is missing. ([#57])
- **`manifest.tool_version` populated.** `cmd/xray/run.go` never set `run.Options.ToolVersion`, so every artifact shipped with an empty `tool_version` in both `manifest.json` and the `_schema` row. The `-ldflags`-injected `version` now flows through. ([#58])
- **Config validator accepts `<org>/.github`.** The slug regex forbade leading-dot repo names, so `init` → `validate` round-tripped to a diagnostic on the canonical GitHub org-config repo. Owners still must start with `[A-Za-z0-9]`; only the repo half relaxed. ([#59])

[#69]: https://github.com/kmcd/xray/issues/69
[#71]: https://github.com/kmcd/xray/issues/71
[#75]: https://github.com/kmcd/xray/issues/75
[#76]: https://github.com/kmcd/xray/issues/76
[#77]: https://github.com/kmcd/xray/issues/77
[#78]: https://github.com/kmcd/xray/issues/78
[#80]: https://github.com/kmcd/xray/issues/80
[#55]: https://github.com/kmcd/xray/issues/55
[#56]: https://github.com/kmcd/xray/issues/56
[#57]: https://github.com/kmcd/xray/issues/57
[#58]: https://github.com/kmcd/xray/issues/58
[#59]: https://github.com/kmcd/xray/issues/59

## [0.2.0] — 2026-06-06

Coverage + risk hardening on top of v0.1.0. `schema_version` unchanged at 1 — no DDL changes — but several behavioural definitions tighten. The compatibility table maps `0.2.0 → 1`.

### Behaviour changes

- **Rollback detection requires a non-success predecessor.** `linkDeployRollbacks` previously flagged any `(repo, environment)` triple where `D[i].commit_sha == D[i-2].commit_sha`. Routine re-deploys of a green commit (canary advance, autoscaling) tripped this. The heuristic now additionally requires `D[i-1].status != "success"` so the deploy *being* rolled back is the one that failed. See [ADR 017](./docs/adr/0017-rollback-heuristic-tightening.md).
- **Sentry `is_regression` narrowed.** The substring match for `"regression"` across message / title / culprit / tags is removed; `incidents.is_regression` for Sentry rows is now sourced solely from the issue's `isUnhandled` flag. Bugsnag continues to use `reopened_at != nil`. The two definitions are intentionally per-source — analysers should consult `incidents.source` rather than treating the column as cross-source comparable. See [ADR 018](./docs/adr/0018-is-regression-per-connector.md).
- **Defects dedup per `(PR, ref)`.** A ticket reference appearing in both a PR's title and body now emits one `defects` row, not two. `source = "pr_title"` when matched in the title; else `pr_body`. Commit-message refs continue to emit one row per `(commit, ref)`. See [ADR 019](./docs/adr/0019-defects-dedup.md).
- **`merge_method` derivation tightened.** The classifier no longer relies on parent count alone. 2 parents → `merge`; 1 parent with every PR head commit reachable from the merge SHA → `rebase`; 1 parent with not all reachable → `squash`. Reachability is tested via `git merge-base --is-ancestor` against the per-run clone (new `gitcli.Client.IsAncestor` helper). See [ADR 021](./docs/adr/0021-merge-method-derivation.md).

### Test infrastructure

- End-to-end integration test (`internal/run/integration_test.go`) drives `run.Run` against a local bare-repo remote (via git's url-rewrite, no production hook) and a stub connector. Asserts SQLite contents, manifest provenance, postprocess linkage, and failed-connector reporting.
- `internal/gitcli` lifted from 0% to ~90% coverage with a real-git fixture exercising every parser branch — merges, renames, copies, GPG paths, email-only authors, binary numstat, `--shallow-since` boundary.
- `cmd/xray` covered by per-subcommand cobra tests (including the `init` → `validate` round-trip that closes the v0.1.0 review gap). `init.go` gains a `newGitHubClient` package-level hook for stubbing in tests; production behaviour unchanged.
- `internal/model` schema/struct parity test reflect-walks every canonical struct against the DDL; future drift will fail loudly.
- `internal/connectors/github` HTTP-path coverage from ~20% to ~62%: `extractPRs` (including timeline-derived `force_pushed_after_review` and `template_match`), `extractBranches` (including branch-protection 403 graceful degradation), `Ping`, plus adjacent surface — languages, releases, reviews, comments, review-requests, codeowners. PR-commits pagination beyond 100 is pinned by an HTTP-driven test.

### Tooling

- CI coverage gate engaged. `.testcoverage.yml` enforces `package: 50 / total: 70` (per-file gating stays at 0 to avoid noise). Exclusions: `cmd/xray` (CLI glue), `doc.go` files, and the parent packages with no executable code. The connectors are still in the 6–62% range — exhaustive HTTP-path coverage there needs VCR-style fixtures, which are out of scope for v0.2.0.

### Known limitations

- **Honeycomb is repo-agnostic.** Honeycomb has no per-repo dimension, so all deploy markers and SLO burn alerts are attributed to the alphabetically-first repo in the configured set. Multi-repo Honeycomb accounts will see a single repo carry every marker; downstream analysers should treat the `repo` column on `incidents.source = "honeycomb"` and `deploys.source = "honeycomb"` rows as approximate.

## [0.1.0] — 2026-06-05

First tagged release. Emits `schema_version` 1.

### Schema

- Established the canonical model (29 tables; see [`docs/schema.md`](./docs/schema.md)).
- `schema_version` 1 introduced; recorded in both `_schema` and `manifest.json`.
- All timestamps stored as UTC ISO-8601 strings; booleans as `INTEGER` 0/1; nullable scalars persisted as SQL `NULL` to signal **unknown** (not *absent*).
- No per-individual-developer aggregation tables, enforced at the schema level.

### Connectors

- **github** — commits + `commit_files` (numstat with rename tracking), commit-body parsing (`is_revert`, `reverts_sha`, `has_hotfix_marker`, `signature_verified`, `landed_via_pr`), `commit_coauthors` (trailers + GitHub API; `kind` heuristic for `human` / `bot` / `ai_tool`), PRs via GraphQL with body-shape counts and `force_pushed_after_review`, reviews, issue + review comments, review requests, labels, `pr_commits`, branches + branch protection (gracefully degrading on 403), CODEOWNERS, repo languages, releases → `deploys`, `file_metrics` (working-tree walk at `head_sha` via go-enry), `harness_artifacts` (AI-tool config inventory + adoption timeline).
- **github_actions** — `builds` + `build_jobs` from the workflow runs / jobs API; `deploys` from the Deployments API. Inherits the GitHub token by default.
- **circleci** — `builds` + `build_jobs` via the v2 pipelines / workflows / jobs endpoints.
- **sentry** — `incidents` with `culprit_ref` from Sentry's own attribution; project-to-repo mapping required.
- **bugsnag** — `incidents`; `culprit_ref` emitted as `NULL` per spec (Bugsnag's top stack frame is not an equivalent).
- **honeycomb** — `deploys` from deploy markers; optional `incidents` from SLO burn alerts.

### Behaviour

- CLI surface: `init`, `validate`, `check`, `run`, `version` (cobra-based).
- `validate` produces line-referenced diagnostics matching the spec format.
- `check` performs live preflight: per-connector `Ping`, per-repo `git ls-remote`, plus `git`-on-PATH verification.
- `run` orchestrates per-repo clone (shallow-since `window.Start - 30d`), worker-pooled connector dispatch, SQLite write, manifest assembly, and `.tar.gz` packaging.
- `manifest.json` carries the full `extraction_provenance` block per the spec, including per-endpoint `accessible` flags so absence-because-inaccessible can be distinguished from absence-because-no-signal.
- Defects parsed from PR titles, PR bodies, and commit messages (`<PREFIX>-<N>` and `#<N>` patterns).
- Post-extraction linkage: incidents resolved to deploys by `release_ref`; deploy rollback heuristic linking `supersedes_deploy_id` + `rolled_back`.
- `capture_harness_content` config toggle controls whether AI-tool config file contents are persisted (default `false` keeps the no-source-content guarantee).
- HTTP traffic across all connectors routed through a 3-attempt / 60s cumulative-budget retry transport honouring `Retry-After` and `X-RateLimit-Reset`.
- Connectors are strictly read-only; tokens never logged.

### Release engineering

- Cross-compiled binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`. CGO disabled across the board.
- `checksums.txt` signed by cosign in keyless mode against the GitHub OIDC issuer; verification snippet in the [README](./README.md#install).
- CI gates: build + test (Ubuntu + macOS), lint (`golangci-lint` v2 with `gosec`), `govulncheck`, `go-test-coverage`.

[0.2.2]: https://github.com/kmcd/xray/releases/tag/v0.2.2
[0.2.1]: https://github.com/kmcd/xray/releases/tag/v0.2.1
[0.2.0]: https://github.com/kmcd/xray/releases/tag/v0.2.0
[0.1.0]: https://github.com/kmcd/xray/releases/tag/v0.1.0
