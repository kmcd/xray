# Changelog

All notable changes to `xray` per release. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [semver](https://semver.org/) on the binary, while `schema_version` (in `manifest.json` and the `_schema` SQLite table) is an integer bumped only on breaking changes to the output model.

The analyser refuses to load artifacts at an unknown `schema_version`. See the [compatibility table](./README.md#compatibility) in the README for the binary-to-schema mapping.

## [0.2.1] — 2026-06-06

Five bug fixes surfaced by the v0.2.0 smoke test against `goreleaser/chglog`. No schema change; `schema_version` stays at 1.

Verified against `goreleaser/chglog` post-fix (~18-month window): 65 commits with non-zero numstat, 64 PRs with shape signals, 6 releases each with their own tag-resolved commit SHA, `tool_version` populated, no slug diagnostic on `<org>/.github`.

### Fixes

- **`gitcli` numstat preserved.** `git log --numstat --name-status` silently dropped numstat output on modern git (`--name-status` wins). Every `commits.additions` / `commits.deletions` / `commit_files.additions` / `commit_files.deletions` was 0 — hotspot and change-size analysis was impossible. Parser switched to `--numstat --raw`, which compose. Regression guard added in `internal/gitcli/gitcli_test.go`. ([#55])
- **`releases` filtered by window.** `extractReleases` shipped every release in the repo's history regardless of the configured window — the smoke run produced 19 releases dating back to 2020. Now skipped per `window.Contains` with early-stop paging on the created-at-desc ordering. ([#56])
- **`releases` / `deploys` get the tagged commit, not HEAD.** `resolveReleaseSHA` resolved `r.TargetCommitish` (typically a branch like `main`), so `GetCommitSHA1` returned the branch HEAD and every release on the same default branch stamped the same SHA. Now resolves the tag itself; falls back to `TargetCommitish` only when the tag is missing. ([#57])
- **`manifest.tool_version` populated.** `cmd/xray/run.go` never set `run.Options.ToolVersion`, so every artifact shipped with an empty `tool_version` in both `manifest.json` and the `_schema` row. The `-ldflags`-injected `version` now flows through. ([#58])
- **Config validator accepts `<org>/.github`.** The slug regex forbade leading-dot repo names, so `init` → `validate` round-tripped to a diagnostic on the canonical GitHub org-config repo. Owners still must start with `[A-Za-z0-9]`; only the repo half relaxed. ([#59])

[#55]: https://github.com/kmcd/xray/issues/55
[#56]: https://github.com/kmcd/xray/issues/56
[#57]: https://github.com/kmcd/xray/issues/57
[#58]: https://github.com/kmcd/xray/issues/58
[#59]: https://github.com/kmcd/xray/issues/59

## [0.2.0] — 2026-06-06

Coverage + risk hardening on top of v0.1.0. `schema_version` unchanged at 1 — no DDL changes — but several behavioural definitions tighten. The compatibility table maps `0.2.0 → 1`.

### Behaviour changes

- **Rollback detection requires a non-success predecessor.** `linkDeployRollbacks` previously flagged any `(repo, environment)` triple where `D[i].commit_sha == D[i-2].commit_sha`. Routine re-deploys of a green commit (canary advance, autoscaling) tripped this. The heuristic now additionally requires `D[i-1].status != "success"` so the deploy *being* rolled back is the one that failed. See ADR 017.
- **Sentry `is_regression` narrowed.** The substring match for `"regression"` across message / title / culprit / tags is removed; `incidents.is_regression` for Sentry rows is now sourced solely from the issue's `isUnhandled` flag. Bugsnag continues to use `reopened_at != nil`. The two definitions are intentionally per-source — analysers should consult `incidents.source` rather than treating the column as cross-source comparable. See ADR 018.
- **Defects dedup per `(PR, ref)`.** A ticket reference appearing in both a PR's title and body now emits one `defects` row, not two. `source = "pr_title"` when matched in the title; else `pr_body`. Commit-message refs continue to emit one row per `(commit, ref)`. See ADR 019.
- **`merge_method` derivation tightened.** The classifier no longer relies on parent count alone. 2 parents → `merge`; 1 parent with every PR head commit reachable from the merge SHA → `rebase`; 1 parent with not all reachable → `squash`. Reachability is tested via `git merge-base --is-ancestor` against the per-run clone (new `gitcli.Client.IsAncestor` helper). See ADR 021.

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

[0.2.1]: https://github.com/kmcd/xray/releases/tag/v0.2.1
[0.2.0]: https://github.com/kmcd/xray/releases/tag/v0.2.0
[0.1.0]: https://github.com/kmcd/xray/releases/tag/v0.1.0
