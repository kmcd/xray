# Architecture Decision Records

| # | Title | Status | Summary |
|---|---|---|---|
| [0001](0001-record-architectural-decisions.md) | Record architectural decisions | accepted | We adopt ADRs for non-trivial design choices |
| [0002](0002-go-sum-not-committed-in-scaffolding.md) | `go.sum` not committed in v0.1.0 scaffolding | superseded by ADR 013 | Unblocks parallel agent work; `go.sum` committed in ADR 013 |
| [0003](0003-library-set-locked-v0.1.0.md) | Library set locked at v0.1.0 | accepted | Pure-Go dependency set pinned: cobra, toml, sqlite, go-github, githubv4, oauth2, go-enry, backoff, slog |
| [0004](0004-parallel-agent-fan-out-no-worktrees.md) | Parallel agent fan-out, no worktrees | accepted | Agents write files in the same checkout; main commits; per-directory ownership minimises races |
| [0005](0005-skip-branch-protection-v0.1.0.md) | Skip branch protection in v0.1.0 | accepted | Deferred to post-v0.1.0; conflicts with direct-to-main workflow |
| [0006](0006-version-command-in-m0.md) | `xray version` in M0, not M1 | accepted | Minimal `xray version` in M0 using stdlib `flag`; cobra replaces it in M1 |
| [0007](0007-connector-interface-shape.md) | Connector interface shape | accepted | `Name()`, `Ping(ctx)`, `Extract(ctx, repo, window, sink) Provenance`; typed sinks; provenance as return value |
| [0008](0008-is-dependency-manifest-heuristic.md) | `is_dependency_manifest` heuristic set | accepted | Static filename allowlist; no build-system detector |
| [0009](0009-harness-artifact-tool-detection.md) | Harness-artifact tool detection from path | accepted | Path-based static table; no content sniffing |
| [0010](0010-cosign-keyless-signing.md) | Cosign keyless signing | accepted | OIDC keyless signing of `checksums.txt`; no long-lived key |
| [0011](0011-schema-version-1-at-v0.1.0.md) | Schema version = 1 at v0.1.0 | accepted | Initial `schema_version = 1`; bumped only on breaking changes |
| [0012](0012-commit-style.md) | Commit style | accepted | English imperative; no co-author trailer; no emojis |
| [0013](0013-go-directive-1.26.4.md) | Go directive bumped to 1.26.4 | accepted | Six stdlib CVEs required 1.26.4; `go.sum` committed |
| [0014](0014-golangci-lint-v2.md) | golangci-lint v2 + lint config tuning | accepted | v1 incompatible with Go 1.26; v2 schema rewrite with gosec enabled |
| [0015](0015-three-ci-gates.md) | Three additional CI gates | accepted | lint + govulncheck + coverage; mirrors gauge_intelligence Ruby toolchain |
| [0016](0016-agentic-infra-from-gauge-intelligence.md) | Agentic infra adopted from gauge_intelligence | accepted | `/ready`, `diff_review.md`, `agent_prompt_template.md`, `bin/ship` adopted |
| [0017](0017-rollback-heuristic-tightening.md) | Rollback heuristic tightening | accepted | Requires `D[i-1].status != "success"` to avoid false positives on re-deploys |
| [0018](0018-is-regression-per-connector.md) | `is_regression` per-connector, with Sentry narrowed | accepted | Each connector emits its own `is_regression`; analyser unifies via `incidents.source` |
| [0019](0019-defects-dedup.md) | Defects dedup per `(PR, ref)` | accepted | One row per `(PR, ticket_ref)`; `source = pr_title` if matched in title, else `pr_body` |
| [0020](0020-honeycomb-first-repo-wins.md) | Honeycomb first-repo-wins | accepted | Known v1 limitation; all markers attributed to first repo seen |
| [0021](0021-merge-method-derivation.md) | `merge_method` derivation tightened | accepted | Parent count + commit reachability; replaces parent-count-only heuristic |
| [0022](0022-coverage-thresholds-revised.md) | Coverage thresholds revised | accepted | `total: 50 / package: 0`; per-package gating returns in v0.3.x with VCR fixtures |
| [0023](0023-author-handles-hashed-at-boundary.md) | Author handles hashed at the boundary | accepted | `*_handle` columns emit opaque `h_<15 digits>` tokens; bumps schema_version 1→2 |
| [0024](0024-github-pr-enrichment-inline-graphql.md) | GitHub PR enrichment as inline GraphQL connections | accepted | Eliminates 20–40k extra round-trips; reviews/comments/threads inline in `prListQuery` |
| [0025](0025-prefetcher-opt-in-interface.md) | Prefetcher: opt-in interface | accepted | Optional `connector.Prefetcher` overlaps PR fetch with clone; ~150 s saved on 7-day smokes |
| [0026](0026-github-prefetch-resilience.md) | GitHub Prefetch resilience | accepted | `queryWithEOFRetry` + `costInterceptor` body-read fix; cursor-handoff on partial cache |
| [0027](0027-go-vcr-v4-for-http-replay.md) | go-vcr/v4 for HTTP replay fixtures | accepted | VCR cassettes under `testdata/cassettes/`; `ModeReplayOnly` in CI |
| [0028](0028-graceful-ctrl-c.md) | Graceful Ctrl-C | accepted | Two-state signal handler; first signal cancels, second force-exits; exit code 130 |
| [0029](0029-three-layer-sensor-architecture.md) | Three-layer sensor architecture | accepted | `make gates` (linters) + `make sweep` (quarterly deadcode/nilaway/gocritic) + `make mutation-audit` (per-release gremlins) |
| [0030](0030-honeycomb-marker-cache.md) | Honeycomb marker cache | accepted | 24-hour TTL disk cache under `$UserCacheDir/xray/honeycomb/`; cuts repeat-run wall-clock from ~22s to <1s; `--no-cache` opt-out |
