# ADR 0033: Canonical deploy status vocabulary

**Status:** Accepted
**Date:** 2026-06-18
**Issue:** #180

## Context

xray emits `deploys.status` ∈ `{success, failed, in_progress}`. The assay canonical-schema had a different enum (`{succeeded, failed, rolled_back}`) and five filter sites in `performance.py` that matched on `succeeded`. Real xray data produced no rows at those sites. Issue #180 required resolving the discrepancy.

A second concern: `rolled_back` appearing as a status value duplicates the existing `rolled_back BOOLEAN` column and `supersedes_deploy_id` foreign key, creating two representations of one fact.

## Decision

The canonical `deploys.status` set is **`{success, failed, in_progress}`** (xray's existing vocabulary). The spec was the side that was wrong; xray emits no change.

Three reasons, any one sufficient:

1. xray already does the right thing. `deploys.go` normalises heterogeneous sources (GitHub Deployments API, GitHub Releases, Honeycomb markers) onto a small terminal/non-terminal set. This is correct design.

2. The assay enum was semantically defective in two ways:
   - `rolled_back` as a status duplicates the existing boolean column. Rollback is orthogonal to terminal outcome (a deploy can succeed and later be rolled back).
   - No non-terminal value. Real deploys are non-terminal during extraction (rails/rails: 36 of 43 `environment=release` records are `in_progress`). An enum without `in_progress` forces data loss.

3. Lowest blast radius. The fix is contained to one consumer (assay) already producing wrong results on real data.

**The settled contract:**

| Aspect | Value |
|--------|-------|
| `deploys.status` | `success` / `failed` / `in_progress` |
| Rollback | `rolled_back` BOOLEAN column; never a status value |
| DORA terminal counts | count `success`; exclude `in_progress`; CFR denominator = `success + failed` |

## Consequences

- assay's `canonical-schema.md`, `performance.py` (5 sites), and Beacon fixtures are updated to match xray's vocabulary.
- xray emits no changes to the status enum.
- `is_prerelease INTEGER NOT NULL DEFAULT 0` added to `deploys` (non-breaking, rides this issue).
