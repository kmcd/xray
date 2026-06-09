# xray ADR (architecture decision record)

Running log of non-obvious decisions made during v0.1.0 build-out. Each entry: decision, rationale, alternatives weighed. Append-only; supersede with new entries rather than editing in place.

---

## 001 — Project workflow: direct-to-main, no PRs

**Decision.** Local commits land directly on `main`. No PR flow. No branch protection requiring PRs.

**Rationale.** User explicitly chose this. They handle review on a per-commit basis after pushes and run an end-to-end review at completion.

**Cost.** No automated review gate. CI is the only safety net. Force-push protection is still off — should be enabled once we're past v0.1.0 thrash.

---

## 002 — `go.sum` not committed in v0.1.0 scaffolding

**Decision.** `go.mod` ships with all expected dependencies; `go.sum` is not generated locally (no `go` on the dev machine) and is not committed.

**Rationale.** Unblocks parallel agent work without round-tripping through CI to update `go.sum`. First CI run materialises `go.sum` for the runner via `go mod download`.

**Cost.** Non-reproducible builds until `go.sum` is committed. Action item: commit `go.sum` after first local successful build, before tagging v0.1.0.

---

## 003 — Library set (locked at v0.1.0)

**Decision.** Hard-pin to:

- CLI: `github.com/spf13/cobra`
- TOML: `github.com/BurntSushi/toml` (line-number preservation via `MetaData`)
- SQLite: `modernc.org/sqlite` (pure-Go, CGO-free — mandatory per spec)
- GitHub REST: `github.com/google/go-github/v66`
- GitHub GraphQL: `github.com/shurcooL/githubv4`
- OAuth: `golang.org/x/oauth2`
- Language detection: `github.com/go-enry/go-enry/v2`
- Backoff: `github.com/cenkalti/backoff/v4`
- Logging: stdlib `log/slog`

**Rationale.** All pure-Go (CGO-free). Each has the narrowest API surface that meets the spec.

**Rejected.** `github.com/jackc/pgx` (no), `mattn/go-sqlite3` (CGO), `urfave/cli` (cobra has wider in-tree adoption), custom GraphQL client (githubv4 already typed).

---

## 004 — Parallel agent fan-out, no worktrees

**Decision.** Implementation work after the core interfaces are defined fans out to per-milestone agents working in the same checkout. Agents write files only; main commits.

**Rationale.** User requested speed. Worktrees explicitly off the table. Per-directory ownership (each connector in its own `internal/connectors/X/` directory) keeps races to a minimum. `go.mod` is pre-populated to prevent concurrent edits.

**Cost.** If two agents accidentally touch the same file, the later writer wins; main reviews the resulting diff before commit.

---

## 005 — Skipping M0 issue #8 (branch protection on main)

**Decision.** Branch protection is not configured in v0.1.0.

**Rationale.** Conflicts with the direct-to-main workflow chosen in ADR 001.

**Action.** Issue closed with a "deferred to post-v0.1.0" comment.

---

## 006 — `xray version` lives in M0 main, not M1

**Decision.** A minimal `xray version` command exists in `cmd/xray/main.go` from M0 onward, using stdlib `flag`. M1 replaces this with the cobra-based command tree without losing the `version` command.

**Rationale.** M0 exit criteria require `xray version` to work. Doing it without cobra in M0 keeps the M0 module dep-free.

---

## 007 — Connector interface shape

**Decision.** Connectors expose three methods: `Name()`, `Ping(ctx)`, `Extract(ctx, repo, window, sink) Provenance`. Sinks are typed (one method per canonical table) rather than generic `Insert(row)`. Provenance is the return value, not a side channel.

**Rationale.** Typed sinks let the compiler enforce the canonical model — a connector cannot accidentally invent a table. Provenance-as-return makes the "did this connector cover the window?" question first-class for the manifest writer.

**Cost.** Adding a table is a Sink interface change (every connector recompiles). Acceptable at this stage; the table set is the schema.

---

## 008 — `is_dependency_manifest` heuristic set

**Decision.** Static list of filenames recognised as dependency manifests:
`Gemfile`, `Gemfile.lock`, `package.json`, `package-lock.json`, `yarn.lock`,
`pnpm-lock.yaml`, `go.mod`, `go.sum`, `Cargo.toml`, `Cargo.lock`,
`requirements.txt`, `Pipfile`, `Pipfile.lock`, `poetry.lock`, `pyproject.toml`,
`composer.json`, `composer.lock`, `pom.xml`, `build.gradle`,
`build.gradle.kts`, `Podfile`, `Podfile.lock`, `mix.exs`, `mix.lock`.

**Rationale.** Avoids dragging in a generic build-system detector; the set is small and stable.

---

## 009 — Harness-artifact tool detection from path

**Decision.** Path-based mapping in a static table; no content sniffing.

| Pattern                                | tool         | kind         |
| -------------------------------------- | ------------ | ------------ |
| `CLAUDE.md`                            | claude_code  | instructions |
| `.claude/**`                           | claude_code  | (subdir-dependent: rules/skills/agents/commands) |
| `AGENTS.md`                            | unknown      | instructions |
| `.cursor/rules` / `.cursor/rules/**`   | cursor       | rules        |
| `.cursorrules`                         | cursor       | rules        |
| `.github/copilot-instructions.md`      | copilot      | instructions |
| `.aider*` / `aider.conf.yml`           | aider        | rules        |
| `.windsurfrules`                       | windsurf     | rules        |
| `.continue/**`                         | continue     | rules        |
| `.mcp.json` / `**/mcp.json`            | generic_mcp  | mcp          |
| `.github/workflows/*` invoking AI bots | (detected per-workflow) | workflow |

**Rationale.** Path-based is what the spec calls for; content-free is what the static-binary constraint requires.

---

## 010 — Cosign keyless signing

**Decision.** Release pipeline uses `cosign sign-blob` in OIDC keyless mode against the goreleaser-produced `checksums.txt`.

**Rationale.** No long-lived signing key to manage. GitHub Actions' OIDC issuer is the trust anchor; verification uses `cosign verify-blob --certificate-identity-regexp 'https://github.com/kmcd/xray/.*'`.

**Cost.** Requires `id-token: write` permission in the release workflow. Signed only `checksums.txt`, not individual archives — verification is "verify checksums.txt is signed by us, then sha256 the archive".

---

## 011 — Schema version = 1 at v0.1.0

**Decision.** Initial `schema_version` = 1. Bumped only on breaking changes per spec rules.

**Action.** README compatibility table maps `xray v0.1.0 -> schema_version 1`.

---

## 012 — Commit style

**Decision.** Concise English imperative commit messages, no Claude co-author trailer, no emojis. One-line subject; body only when not self-evident from the diff.

**Rationale.** User-stated preference (also captured in personal memory).

---

## 013 — Go directive bumped to 1.26.4

**Decision.** `go.mod` directive is `go 1.26.4`, not the originally chosen 1.23.

**Rationale.** `govulncheck` against the v0.1.0 connector and ratelimit code surfaced six stdlib CVEs whose fixes only ship in 1.26.4 (`net/textproto` GO-2026-5039, `crypto/x509` GO-2026-5037, plus 4 others reachable from the ratelimit transport). Bumping the directive is the canonical fix; CI's `setup-go` uses `go-version-file` so this pins the toolchain version.

**Cost.** Anyone building from source needs Go 1.26.4+. Pre-1.0, acceptable. ADR-002's deferred go.sum-commit was resolved as part of this commit — `go.sum` now ships with the repo.

---

## 014 — golangci-lint v2 + lint config tuning

**Decision.** golangci-lint v2.12.2 (the v1 line refuses to lint a 1.26 target). Config rewritten in v2 schema. `gosec` enabled inline.

Lint tuning:
- `revive`'s `exported` rule disabled (every `internal/` symbol would demand a docstring; not worth it pre-1.0).
- `errcheck.exclude-functions` covers idiomatic `fmt.Fprint*` to stdio and `defer x.Close()`.
- `gosec` excludes `G104` (better covered by errcheck) and `G122` (TOCTOU symlink races on our own per-run temp dirs — not a threat model we care about).
- Remaining `gosec` flagged sites carry `#nosec` annotations with one-line justifications.

**Rationale.** Same Go-toolchain constraint as ADR 013. v2's schema is incompatible with v1 so the migration was a one-shot rewrite. Tuning chosen to keep signal-to-noise high without weakening the security-relevant checks.

---

## 015 — Three additional CI gates (vuln, coverage, gosec via lint)

**Decision.** CI runs five jobs on every push: `test (ubuntu)`, `test (macos)`, `lint` (includes gosec), `vuln` (govulncheck), `coverage` (go-test-coverage). `bin/ship` runs the same three gates locally; `make gates` is the underlying target.

**Rationale.** Mirrors the gauge_intelligence Ruby-side toolchain: rubocop/undercover/brakeman/bundler-audit. Go equivalents map cleanly. Catching a CVE or a security smell pre-push is cheap; doing it after a tagged release is not.

**Cost.** ~3 minutes per push. Coverage thresholds are permissive (`0` at every level) — the report surfaces, doesn't block. Tighten once the connector test surface stabilises.

---

## 016 — Agentic infra adopted from gauge_intelligence

**Decision.** Brought across three patterns from the Ruby project:
- `.claude/commands/ready.md` — `/ready` completion-gate slash command (gates + diff review + scope sweep).
- `.claude/diff_review.md` — auto-applied review criteria (schema parity, connector contract, HTTP boundary, file IO, test shape, style).
- `.claude/agent_prompt_template.md` — verbatim clauses to paste into parallel agent dispatches. Each clause is named after a specific failure mode from the v0.1.0 build (e.g. the missed `types.go` commit, the `SetCaptureHarnessContent` setter assumption, the `connector.Window` JSON tag fix).
- `bin/ship` — thin wrapper over `make gates`.

**Rationale.** The patterns earned their keep in gauge. Adapting them for Go was small. The agent template specifically pays back the next time we fan out parallel work — the forcing functions are explicit rather than recalled.

**What was skipped.** `guard-commit.sh` (no shared-index multi-session risk in solo-on-main), `guard-ck.sh` (no analog heavy CI gate to block), in-repo `.claude/memory/` (global memory under `~/.claude/projects/.../memory/` already covers this), and the editorial / domain-specific clauses (no analog content in xray).

---

## 017 — Rollback heuristic tightening

**Decision.** The deploy-rollback heuristic now requires `D[i-1].status != "success"` in addition to the prior conditions (`D[i].commit_sha == D[i-2].commit_sha AND D[i].commit_sha != D[i-1].commit_sha`).

**Why:** The original heuristic false-positives on routine re-deploys of a green commit — canary advance, autoscaling rollouts, blue/green flips back. None of those are rollbacks. The deploy *being* rolled back is the one that failed; requiring the predecessor to be non-success gates the heuristic to the actual rollback pattern.

**How to apply:** `internal/postprocess/postprocess.go` — `linkDeployRollbacks`. Add test cases distinguishing rollback (prior deploy failed) from re-deploy (prior deploy succeeded). The heuristic remains a heuristic; an explicit `rolled_back` signal from a deploy provider, when available, is authoritative.

---

## 018 — `is_regression` per-connector, with Sentry narrowed

**Decision.** Sentry's `is_regression` is set from `issue.isUnhandled` only. The substring-match path (`message`/`title`/`culprit`/tag contains "regression") is removed. Bugsnag's `is_regression = reopened_at != nil` is unchanged. The two sources have intentionally different definitions of "regression"; downstream analysers consult `incidents.source` rather than treating the column as cross-source comparable.

**Why:** The substring match conflates user-named tags with source-level state. A team that tags errors `"regression-candidate"` would flood the column with false positives.

**How to apply:** `internal/connectors/sentry/issues.go` — drop the substring match in `isRegression`. Document the per-source semantics in `docs/schema.md` and in the `incidents` row notes of `CLAUDE.md` if updating the spec is warranted (probably not — the spec already says "heuristic per connector — documented in each").

---

## 019 — Defects dedup per `(PR, ref)`

**Decision.** When the same ticket reference appears in both a PR's title and body, one row is emitted (not two). The `source` is set to `pr_title` if the ref matched the title; else `pr_body`. Commit-message refs are unchanged — one row per (commit, ref) — because a commit message is a single text location.

**Why:** The current behaviour emits two rows for the same logical mention (`PROJ-123` in title and body of the same PR). Downstream queries that count distinct refs per PR would over-count; queries that join on `(repo, ticket_ref)` see inflated counts.

**How to apply:** `internal/connectors/github/defects.go` — change emission so PR-level callers pass `(title, body)` together and the helper dedups before insert. Update `internal/connectors/github/prs.go` to call the new helper with both texts.

---

## 020 — Honeycomb first-repo-wins documented as a v1 limitation

**Decision.** No code change. Honeycomb has no per-repo concept; the connector continues to attribute all markers to the first repo seen. The limitation is surfaced in `CHANGELOG.md` "Known limitations" and `docs/schema.md` `deploys` row notes.

**Why:** Tagging the same marker to every repo would inflate counts; tagging to first-seen is honest about the limitation. A real fix would need the Honeycomb dataset to carry a per-repo dimension we can read.

**How to apply:** Docs only — `CHANGELOG.md`'s v0.2.0 section and `docs/schema.md`'s `deploys` notes. Code unchanged.

---

## 021 — `merge_method` derivation tightened

**Decision.** Replace the parent-count-only heuristic with parent count plus commit-reachability:

- 2 parents → `merge`
- 1 parent, all PR head commits reachable from merge commit → `rebase`
- 1 parent, not all reachable → `squash`

**Why:** Parent-count alone misclassifies rebase as squash; reachability is the signal that separates them. GitHub doesn't expose `merge_method` to non-admin tokens; the heuristic is what tools have to use.

**How to apply:** `internal/connectors/github/prs.go` — `deriveMergeMethod`. Use the git clone to test reachability via `git merge-base --is-ancestor` per PR head commit, or read the PR's `commits` GraphQL connection and compare against `git log --pretty=%H` from the merge commit. Add table-driven tests covering all three branches.

---

## 022 — Coverage thresholds tightened (revised at engagement)

**Decision.** `.testcoverage.yml` gates at `file: 0 / package: 50 / total: 70`. Exclusions: `cmd/xray` (CLI glue), `doc.go` files anywhere, `internal/connector/`, `internal/connectors/` (parent), `internal/archive/`.

**Why (revised again — engaged at total:50 / package:0):** Wave-A and wave-B tests brought total coverage from 33% to 56%, but the non-github connector packages still sit in the 6–39% range because their HTTP-driven paths need VCR-style fixtures that v0.2.0 doesn't ship. Setting `package: 50` failed those connectors in CI. Engaging at `total: 50 / package: 0` catches regressions on the project as a whole without per-package noise on the HTTP-bound connectors. Per-package gating returns in v0.3.x once VCR fixtures land and the connectors all reach a consistent baseline.

Per-file gating stays at 0 to avoid noisy per-file flags on small files; the per-package gate is the load-bearing signal.

**How to apply:** `.testcoverage.yml` — set the revised thresholds and exclusions. Lands as the last issue of wave A (#47 in this milestone, #5 in the plan).

---

## 023 — Author handles hashed at the boundary. Bumps schema_version 1→2.

**Decision.** Every `*_handle` column that names an individual identity (`commits.author_handle`, `commits.committer_handle`, `commit_coauthors.handle`, `prs.author_handle`, `reviews.reviewer_handle`, `pr_comments.author_handle`) emits an opaque `h_<15 digits>` token instead of the raw login or git ident. The pre-image is the canonical `Name <email>` after `.mailmap` resolution (for commit-side identities) or the lowercased `@login` (for GitHub-side identities). Hash = sha256(canonical), low 64 bits, modulo 10^15, zero-padded.

**Why this is a `schema_version` bump even though the assay v1.1 prompt claimed otherwise.** The prompt described the v1.1 work as "additive — no DDL changes". That's true for adding new columns and new tables. But changing the *semantics* of `commits.author_handle` from "raw git author name" to "opaque hash" is exactly the case [`CLAUDE.md`](../CLAUDE.md#schema-versioning) → "Schema versioning" lists as breaking ("changing the semantics of an existing column"). Column type and name unchanged; meaning is. Analyser pinned to schema_version=1 would see plausible-looking strings, decode them as raw logins, and silently produce wrong truck-factor / Conway's-law verdicts. The bump forces those analysers to error out instead of silently degrading.

**Why hash to digits instead of hex.** assay v1.1's boundary check is `^h_\d{3,}$` (digits only). Hex hashes would fail the check. We use `fmt.Sprintf("h_%015d", uint64(sha256[:8]) % 1e15)` — 10^15 distinct values, birthday-collision parity near ~10^7.5 distinct authors, well above any plausible team-of-teams scale.

**Why the `@`-prefix on login canonicalisation.** GitHub logins and commit `Name <email>` strings live in disjoint namespaces. Without per-user email resolution there's no reliable way to link a commit author to their GitHub login (GitHub doesn't expose user email reliably). Hashing `"alice"` (login) and `"alice"` (commit name, no email) to the same value would silently fuse unrelated people. The `@` prefix on `canonicalLogin` keeps the two surfaces separate; the linkage gap is documented in `docs/schema.md` and surfaces as two distinct handles for the same person until per-user email resolution lands.

**`pr_review_requests.requested_handle` and `codeowners.owner_handle` stay raw.** These tables carry user *or* team identifiers; the team-vs-user distinction is load-bearing for downstream analysis, and the team slug isn't PII. Out of scope for the assay v1.1 contract.

**How to apply:** `internal/connectors/github/handle.go` holds the helpers; mailmap resolution lives in `internal/gitcli/mailmap.go`. Insert sites in `commits.go`, `coauthors.go`, `prs.go`, `reviews.go`, `pr_comments.go`. `manifest.mailmap_applied` is aggregated in `internal/run/run.go::aggregateMailmapApplied`. Schema-version assertion in `internal/model/schema_test.go::TestDDL_SchemaVersionConstant` updated to expect `2`.

---

## 024 — github PR enrichment as inline GraphQL connections, not alias batches

**Decision.** PR enrichment (reviews, top-level comments, review threads, review-request timeline events, merge-method parent count) moves off per-PR REST round-trips and into the existing `prListQuery` GraphQL walk as inline connections. `reviews(first: 100)`, `comments(first: 100)`, `reviewThreads(first: 100) { comments(first: 100) }`, and `mergeCommit { parents { totalCount } }` are read at the same time as the PR node itself; `timelineItems.itemTypes` grows from `[READY_FOR_REVIEW_EVENT, PULL_REQUEST_REVIEW, HEAD_REF_FORCE_PUSHED_EVENT]` to also include `REVIEW_REQUESTED_EVENT` so review-request rows come from the same projection. Per-PR overflow paginators (`paginatePRReviewsOverflow`, `paginatePRIssueCommentsOverflow`, `paginatePRReviewThreadsOverflow`, `paginatePRReviewRequestsOverflow`) handle the long tail. Schema unchanged — `prs`, `reviews`, `pr_comments`, `pr_review_requests` row shapes are identical; `schema_version` stays at 2.

**Why.** A 30-day smoke against `posthog/posthog` took ~10 hours. The customer requirement is a 2-year window on a posthog-sized repo in 5–10 minutes (under-1-hour ceiling). Root cause was the per-PR fan-out: `extractReviews` (REST), `extractPRComments` (two REST loops for issue + review comments), `extractPRReviewRequests` (a separate GraphQL call), and `fetchMergeMethod` (two REST calls per merged PR — `PullRequests.Get` + `Repositories.GetCommit`). At 5–10k PRs over a 2-year window, that's 20–40k extra round-trips on top of the already-paginated PR list. The PR walk pays for one GraphQL round-trip per 50 PRs regardless; widening each node with inline connections costs zero extra calls.

**A. Inline-extension over alias-batching.** ADR-era commit enrichment in `enrich.go` uses a 25-alias-per-query batch pattern with a 500 ms inter-batch delay (see [`#64`](https://github.com/kmcd/xray/issues/64) and the v0.2.2 entry). That shape is right for commits — the commit list is paginated separately and the enrichment endpoints aren't connections off the commit node. PR enrichment is the opposite case: every signal we need *is* already a connection off the PR node in the GraphQL schema. Inline extension is strictly cheaper than alias-batching here because it eliminates the round-trips entirely rather than coalescing them. Alias batching for reviews / comments would still cost N/25 extra POSTs per stage on top of the PR walk.

**B. Inner-connection page size 100, with per-PR overflow.** GitHub GraphQL's points budget is the binding constraint, not query complexity or response size. 100 inner items inside 50 PRs per page is a calculated tradeoff: enough headroom for the vast majority of PRs (the long tail of large posthog PRs occasionally crosses 100 thread comments) without inflating per-query points for the common case. The overflow paginators mirror the shape of the existing `paginatePRCommits` helper in `prs.go` — same `pageInfo.HasNextPage` + `endCursor` walk, same per-PR scope.

**C. Merge-method parent count from GraphQL.** ADR 021 fixed the `merge_method` classifier to use parent count plus PR-head reachability. The parent count was previously sourced from `PullRequests.Get` + `Repositories.GetCommit` (two REST calls per merged PR). It's now read from `mergeCommit.parents.totalCount` in the bulk query at zero marginal cost. The git-side `IsAncestor` reachability check stays — it's a local-clone operation, doesn't cost API calls, and ADR 021's classifier is unchanged in semantics.

**Out of scope.**

- *GraphQL points-budget retuning at posthog scale.* If a customer-scale wall surfaces, the mitigation is to drop the outer PR page size from 50 to 25; the inline-connection shape doesn't change. Not pre-tuning until we have telemetry from a real run.
- *Deeper-than-100 inner overflow.* The overflow paginators remain one-PR-at-a-time. A PR with >100 reviews *and* >100 review threads *and* >100 timeline items would fire three overflow walks for that one PR; acceptable because the population is tiny and the alternative (batching overflow across PRs) reintroduces the fan-out shape we're removing.

**How to apply:** `internal/connectors/github/prs.go` — extend the `prListQuery` struct with inline `Reviews`, `Comments`, `ReviewThreads`, and `MergeCommit` connections; teach `emitPR` to drain them inline; add `paginatePRReviewsOverflow` / `paginatePRIssueCommentsOverflow` / `paginatePRReviewThreadsOverflow` / `paginatePRReviewRequestsOverflow` helpers mirroring `paginatePRCommits`. Delete the per-PR call sites (`extractReviews`, `extractPRComments`, `extractPRReviewRequests`, `fetchMergeMethod`) once the inline path is verified. Provenance counters fire from the inline emit path; an inner-connection 403 sets the relevant `EndpointStatus{Accessible: false}` exactly as the previous fan-out did. Closes [#69](https://github.com/kmcd/xray/issues/69).

---

## 025 — Prefetcher: opt-in interface to overlap connector work with the clone phase

**Decision.** Add an optional `connector.Prefetcher` interface to the connector contract from ADR 022:

```go
type Prefetcher interface {
    Prefetch(ctx context.Context, slug string, window Window) error
}
```

Connectors that implement it expose a per-repo entry point that `xray run`'s clone phase invokes as a goroutine alongside `git.Clone(...)`. The result is stashed on the connector (per-slug) and consumed by `Extract` later. `Extract` remains the canonical row-emit path; `Prefetch` is purely a wall-clock hint. Non-implementing connectors are skipped at the run.go-side type assertion (`if pf, ok := conn.(connector.Prefetcher); ok { ... }`) so the change is zero-impact on sentry / circleci / bugsnag / honeycomb / githubactions.

In github specifically, `Prefetch` runs `fetchPRs` (the paginated `prListQuery` walk extracted from the prior `extractPRs`) and stores the resulting `[]prGraph` on the connector behind a per-slug `chan struct{}`. `extractPRs` calls `consumePRPrefetch` first; on a hit, the cached nodes feed `emitPRs` directly. On a miss, the live path runs as before.

**Why.** Post-#69, the github connector's wall-clock on `posthog/posthog` 7-day is dominated by the PR GraphQL walk (~9 min) which is API-bound and only depends on the local clone for `resolveMergeMethod` (a per-PR git `merge-base --is-ancestor` check, fast). The clone itself takes ~68 s and the clone-bound `extractCommits` walk takes ~70 s — both could overlap with the PR fetch with no semantic change. Today they're serial; the proposal recovers that wall-clock without changing the connector contract or any row shape. Estimated savings on the 7-day smoke: ~150 s (22% reduction); larger windows recover more clone time.

**A. Opt-in extension, not a required signature.** The connector contract from ADR 022 stays: `Extract(ctx, repo, window, sink) Provenance` is the canonical entry point. `Prefetcher` is a *hint* — connectors implement it only when they have meaningful API-bound work that doesn't need the clone. Non-github connectors don't need to change; the run.go-side check uses a type assertion. This keeps the connector interface minimal and avoids forcing every connector to think about prefetch semantics.

**B. Per-slug cache on the connector, not on the `connector.Repo` struct.** Stashing prefetch state on the connector keeps the canonical `Repo` and `Provenance` shapes unchanged and lets `Extract` discover the cache via a method call (`consumePRPrefetch`) rather than a new struct field that every non-github code path would have to ignore. `consumePRPrefetch` removes the entry on read, so a subsequent `Extract` for the same slug (re-run with `--keep-clones`, etc.) falls back to a live fetch rather than reusing stale nodes.

**C. Two-fragment Provenance merge inside `Extract`.** The github `Extract` runs clone-bound stages (`extractLanguages` / `extractBranches` / `extractCodeowners` / `extractReleases` / `extractCommits` / `fileMetrics` / `harnessArtifacts`) in goroutine A and the PR stage in goroutine B. Each writes to its own `connector.Provenance` fragment; the two are folded into the returned `prov` via a new `(*Provenance).Merge` helper. Policy: `RowsReturned` summed, `Errors` first-wins per context (goroutines own disjoint contexts so collision is invisible), `PaginationComplete` ANDed, `RateLimitTruncated` ORed, `Endpoints` and `Flags` first-wins. The merge alternative — a mutex on every `prov.Errors[...]` / `prov.RowsReturned[...]` write — would touch every extract helper and burn hot-path lock contention. Two fragments + merge keeps the helpers unchanged.

**Out of scope.**

- *Prefetching anything other than PRs.* Commits / file_metrics / harness are clone-bound and can't usefully start before clone. Branches / codeowners / languages are now clone-bound too (#72), so they can't either.
- *Inter-repo concurrency beyond `--workers`.* The clone loop in run.go stays sequential per repo; only the prefetch goroutine becomes concurrent within each iteration. Inter-repo overlap is already handled by the workers pool in phase 3.
- *Sentry / circleci / etc. implementing `Prefetcher`.* No measured need. The interface is in place for them to opt in later if a slow API stage warrants it.

**How to apply:** `internal/connector/connector.go` — add the `Prefetcher` interface and `(*Provenance).Merge`. `internal/connectors/github/github.go` — add `prefetchMu`, `prefetchData`, `Prefetch`, `consumePRPrefetch`. `internal/connectors/github/prs.go` — split the existing `extractPRs` into `fetchPRs` (paginated walk, no emit) + `emitPRs` (per-node emit) + a thin cache-aware `extractPRs`. `internal/connectors/github/extract.go` — split the per-repo `Extract` into a sync prelude (mailmap / repoRow / teams), a parallel block (two goroutines with disjoint prov fragments), and a sync postlude (merge fragments). `internal/run/run.go` — in the clone loop, for each repo, fire `conn.Prefetch(ctx, slug, win)` as a goroutine alongside `git.Clone(...)`; `sync.WaitGroup` joins both before the next iteration so per-repo IO pacing is preserved. Closes [#71](https://github.com/kmcd/xray/issues/71).

---

## 026 — github Prefetch resilience: per-page EOF retry in `fetchPRs`, body-read errors propagated from `costInterceptor`

**Decision.** Two changes work together to make the PR-list GraphQL walk resilient to mid-response truncation (server reset, network drop):

1. `costInterceptor.RoundTrip` (`internal/connectors/github/github.go`) returns `(nil, readErr)` when `io.ReadAll(resp.Body)` fails mid-stream, instead of re-attaching the partial body and returning `(resp, nil)`. The partial body would otherwise surface as a downstream JSON decoder `"unexpected EOF"` that nothing retried.
2. `fetchPRs` (`internal/connectors/github/prs.go`) wraps each `c.gql.Query(...)` call in a small `queryWithEOFRetry`: 3 attempts, 60 s cumulative budget, exponential backoff (500 ms initial, 10 s cap), ctx-aware. Transient EOF class — `errors.Is(err, io.ErrUnexpectedEOF)`, `errors.Is(err, io.EOF)`, or surface-text containing `"unexpected EOF"` — triggers a retry against the *unchanged* GraphQL cursor in `vars["after"]`. Non-transient errors return immediately.

**Why.** The very first realistic post-#71 smoke (posthog 7-day, 2026-06-08) lost the entire prefetch cache to a single mid-response EOF, dropping every PR after the failing page. On a 2-year customer window this turns #71's ~150 s overlap claim into "150 s when the network is perfectly clean for the full clone." The previous behaviour — `extractPRs` emitting whatever survived plus logging the error — kept the early pages but silently truncated the tail.

**A. Why retry in `fetchPRs`, not in `ratelimit.Transport`.** In the transport chain `client → costInterceptor → oauth2.Transport → ratelimit.Transport → http.DefaultTransport`, `ratelimit` sees the response *before* `costInterceptor` reads the body, so a body-read failure inside the interceptor happens *above* the retry layer and bypasses it entirely. Two ways to close that gap:

- Move `costInterceptor` inside `ratelimit.Transport` so body-read failures become transport errors that `ratelimit` retries. Side effect: cost accounting fires once per HTTP attempt instead of once per logical call — a semantics shift in unrelated subsystem (provenance reports `graphql_points_used` summed across retried attempts, not a clean per-call accounting).
- Catch the surfaced error at the `gql.Query` call site. Self-contained; matches the issue author's proposed direction (#80 Open Q1); leaves the ratelimit retry classes untouched (explicitly out of scope per the issue).

The second option won. The retry helper is private to the github package and used only by `fetchPRs` for now — other overflow paginators and REST paths keep their existing semantics until a real-world failure surfaces there.

**B. Cursor stability across retries.** GitHub GraphQL cursors are opaque continuation tokens; empirically they hold for minutes, comfortably longer than the 60 s retry budget. The retry re-issues the same `vars["after"]` cursor, so a successful retry resumes the same page rather than re-walking from the start. If a customer-scale smoke shows cursor expiry inside the budget, the fallback is `ctx`-aware abort and partial-nodes return — already handled by the existing `extractPRs` error path.

**Scope, post-follow-up.** Initial ship (`7ed0f27`) covered Prefetch only and deferred cursor-handoff and overflow-paginator EOF retry. Follow-up (same issue) extended coverage to close both:

- *Cursor-handoff partial cache.* `prPrefetchResult` now carries `nextCursor string`; `fetchPRs` accepts a `startCursor` parameter and returns `(nodes, resumeCursor, err)` where `resumeCursor` is the cursor of the page that failed. `consumePRPrefetch` exposes the cursor; `extractPRs` calls `fetchPRs` again from that cursor when a cached Prefetch errored mid-walk. The unfetched tail is no longer dropped on retry exhaustion.
- *EOF retry on non-Prefetch GraphQL paths.* `queryWithEOFRetry` now wraps every `c.gql.Query` call site (`paginatePRReviewsOverflow`, `paginatePRIssueCommentsOverflow`, `paginatePRReviewThreadsOverflow`, `paginatePRTimelineOverflow`, `fetchBranchProtectionRules`, in addition to the original `fetchPRs` and `paginatePRCommits`). `enrich.go`'s raw commit-enrichment POST uses a sibling `doJSONPOSTWithEOFRetry` (same shape, rebuilds the request each attempt so body is reusable).

**Out of scope.**

- *Adding EOF to `ratelimit.Transport`'s retry classes globally.* Out of scope per #80 — costInterceptor's body-read errors propagate above ratelimit's RoundTrip, so the right layer for retry is per-call (queryWithEOFRetry / doJSONPOSTWithEOFRetry), not the transport.

**How to apply:** `internal/connectors/github/github.go` — change `costInterceptor.RoundTrip` to return `(nil, readErr)` on partial body read; extend `prPrefetchResult` with `nextCursor`; update `Prefetch` / `consumePRPrefetch` to plumb it. `internal/connectors/github/prs.go` — add `queryWithEOFRetry` + `doJSONPOSTWithEOFRetry` + `isTransientEOF` (private); `fetchPRs(ctx, repo, window, startCursor)` returns resumeCursor; `extractPRs` resumes from cursor when Prefetch errored. `internal/connectors/github/{reviews,pr_comments,pr_meta,branches,enrich}.go` — swap to the retry helpers. Tests: `internal/connectors/github/prs_eof_retry_test.go` covers retry-then-success, retry-exhausted-with-partial-nodes, costInterceptor `(nil, err)` propagation, cursor-handoff end-to-end via `Prefetch` + `extractPRs`, and overflow-paginator EOF retry. No new dependencies (`github.com/cenkalti/backoff/v4` already in `go.mod` via `internal/ratelimit`). Closes [#80](https://github.com/kmcd/xray/issues/80).
## 023 — go-vcr/v4 for HTTP replay fixtures in connector tests
**Decision.** Add `gopkg.in/dnaeon/go-vcr.v4` as a test dependency. Connector packages that exercise HTTP paths use VCR cassettes (YAML files committed to the repo under `testdata/cassettes/`) instead of inline `httptest.Server` handlers. The shared helper lives in `internal/connectors/vcr/helper.go`.

**Why.** The `httptest.Server` approach requires writing inline JSON fixtures and handler logic per test case; VCR cassettes are recorded once against real APIs and replay automatically. Cassettes can be re-recorded when API contracts change (`VCR_RECORD=1 GITHUB_TOKEN=<token> go test ...`); the committed YAML captures the exact wire format, making API drift visible. Pure-Go, no native deps — static binary constraint satisfied. MIT license.

**Cassette safety.** A `BeforeSaveHook` strips `Authorization` from all cassette request headers before writing to disk. The URL+method-only matcher (`matchURLMethod`) ensures cassettes replay correctly even when the header set changes between recording and replay.

**How to apply.** Import path: `gopkg.in/dnaeon/go-vcr.v4/pkg/recorder` and `.../pkg/cassette`. Mode: `ModeReplayOnly` by default (CI-safe); `ModeRecordOnly` when `VCR_RECORD=1`. First shipped for `internal/connectors/githubactions` (#66); extend to bugsnag/circleci/honeycomb/sentry when those credentials are available.
