# Changelog

All notable changes to `xray` per release. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows [semver](https://semver.org/) on the binary, while `schema_version` (in `manifest.json` and the `_schema` SQLite table) is an integer bumped only on breaking changes to the output model.

The analyser refuses to load artifacts at an unknown `schema_version`. See the [compatibility table](./README.md#compatibility) in the README for the binary-to-schema mapping.

## [0.2.0]

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

[0.2.0]: https://github.com/kmcd/xray/releases/tag/v0.2.0
[0.1.0]: https://github.com/kmcd/xray/releases/tag/v0.1.0
