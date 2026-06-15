# 0031 — Sparse-historical PR sampling design decisions

**Status:** Accepted
**Context:** Issue #167 — bracketed extraction at reduced API cost

---

## Problem

Full-window PR extraction on dense repos (e.g. the customer scenario behind #150/#151) can run 12–24h wall-clock at 5000 requests/hour per-PAT. The bracketed-rollout engagement SKU needs before-vs-after metrics spanning 5+ years but with full fidelity only around the operator-supplied inflection date. Narrowing `pr_window` (#166) saves cost but amputates the "before" half of the bracket.

## Decisions

### 1. Provenance shape — structured `Sampling` field on `Provenance`

**Decision:** Add `Sampling *SamplingProvenance` to `connector.Provenance` rather than encoding per-bucket data into the existing `ConfigDepth map[string]string`.

**Rationale:** Per-bucket records carry four fields (`target`, `actual`, `total`, `truncated`). Encoding structured data into a string map forces the analyser to re-parse; a typed field makes the contract explicit and survives schema evolution without parse logic.

Non-breaking: "adding a new field to manifest.json" is non-breaking per `CLAUDE.md` schema versioning rules. `Merge()` uses first-wins so the single fragment that owns sampling (Goroutine B) wins cleanly.

A short summary string is also written into `ConfigDepth["pr_history_sample"]` (e.g. `"monthly:20"`) so analyser code that only reads `ConfigDepth` sees signal that the run was sampled.

### 2. Deterministic random seed for `monthly:N:random`

**Decision:** Derive the seed from FNV-64 of `(repo_slug, bucket_month)` rather than a config field or a fixed constant.

**Rationale:**
- **No config field**: operators have no semantic intuition for a seed number; it would be dropped or changed on re-extraction, breaking trend continuity.
- **Not `window.Start.Unix()`**: changing the window shifts every bucket's sample, ruining re-run comparability.
- **Not a fixed constant**: identical seeds across all repos produce correlated bias; different repos would draw the same indices from the same population.
- **FNV-64 of `(slug, month)`**: stable per (repo, month) across re-runs; distinct across repos; cheap; no dependencies.

The seed is not surfaced in provenance (it is fully reproducible from `slug + bucket.Month`).

### 3. Weekly-only recursion when `totalCount > 1000`

**Decision:** When a month bucket's `totalCount` exceeds the GitHub `search()` 1000-result cap, auto-split to 7-day sub-buckets (`"YYYY-MM-Wn"` labels). No further recursion beyond weekly.

**Rationale:**
- Most repos are well under 1000 PRs per month. The 1000+ case is extreme (top-1% of OSS at peak activity).
- Weekly → daily recursion would triple the API cost without meaningfully improving sample quality for the bracketed-rollout analysis questions.
- A repo with >1000 PRs per week gets the truncation warning in provenance (`truncated=true`) and the analyser widens CIs accordingly.

Parent buckets that triggered a split are recorded with `truncated=true` and `nodes=nil`; sub-bucket records follow in `Sampling.Buckets` with `"YYYY-MM-Wn"` labels.

### 4. Sequential sparse path after bracket walk (no concurrent batch handles)

**Decision:** `extractSparsePRs` runs sequentially after `extractPRs` within Goroutine B, not concurrently.

**Rationale:** `emitPR` batch handles are not safe for concurrent invocation. Running the two paths concurrently would require a second set of batch handles or a shared mutex, adding complexity with no wall-clock benefit (both paths are rate-limited by the same GitHub API budget). The sparse bucket goroutines fan out internally (up to 4 concurrent search calls) while the aggregator emits serially.

### 5. `pr_window` and `pr_inflection` are mutually exclusive

**Decision:** Setting both `pr_window` and `pr_inflection` is a validation error.

**Rationale:** Both narrow the PR cluster; composing them would require an explicit precedence rule and surprising behaviour. The operator either wants a fixed date range (`pr_window`) or a bracket-centred extraction (`pr_inflection` + `pr_bracket_window`). Surfacing ambiguity as a diagnostic is safer than silently picking one.
