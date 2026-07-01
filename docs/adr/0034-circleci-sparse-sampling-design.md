# ADR 0034: CircleCI sparse-historical sampling design

**Status:** Accepted
**Date:** 2026-07-01
**Issue:** #203

## Context

Issue #203 added `build_history_sample` to the CircleCI connector — a two-region extraction strategy analogous to GitHub's `pr_history_sample`. Three design decisions with real alternatives were made during implementation.

## Decision 1: single-pass in-memory partition rather than per-bucket API calls

The implementation collects all pipelines in one pagination pass, partitions in memory at `bracketStart`, groups the pre-bracket set by calendar month, and samples N per bucket before fetching workflows/jobs.

**Rationale.** GitHub's sparse walk issues one `search()` API call per month bucket concurrently because GitHub's search API supports `created:YYYY-MM-DD..YYYY-MM-DD`. CircleCI's pipeline API supports only newest-first pagination with an early-stop time. The pipeline-list call is cheap (IDs + timestamps only); the expensive work is the workflow and job fetches that happen per-pipeline. Sampling the in-memory list before those fetches cuts the expensive calls proportionally to the sampling ratio.

**Rejected.** Concurrent per-bucket API calls would require re-paginating from `bracketStart` N times (once per month) with no server-side date filter — more API cost than a single pass. A weekly-split path (mirroring GitHub's 1000-result split) is unnecessary: CircleCI has no search-result cap.

## Decision 2: strategy names `newest_first` / `random` rather than `search_default_relevance` / `random`

**Rationale.** GitHub's default strategy is named after the GitHub search API's relevance ordering, which has no analog in CircleCI. CircleCI pipelines are collected newest-first in a deterministic, predictable order. `newest_first` is accurate and self-documenting; reusing `search_default_relevance` would be misleading to analyser consumers.

## Decision 3: pre-sampling sort `(CreatedAt desc, ID asc)` for determinism

Before sampling each month bucket, `selectPipelines` sorts the group by `(CreatedAt desc, ID asc)`. This applies to both the `newest_first` slice (take first N after sort) and the `random` shuffle (shuffle a deterministically-ordered input).

**Rationale.** CircleCI's pagination order is not guaranteed stable across re-extractions. Without a pre-sort, the `newest_first` slice could return different pipelines on a re-run with the same seed. The sort ensures both strategies are reproducible independent of API pagination jitter, satisfying the "stable re-extractions" requirement.
