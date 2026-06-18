---
name: complexity-history-load-bearing
description: "file_complexity_history is a deliberate signal source (hotspot trajectories) — no opt-out flag, not a perf lever; the (commits × eligible-files) cost is paid for the insight."
metadata: 
  node_type: memory
  type: project
  originSessionId: a741dacc-c9e3-4034-a491-da9423950a2d
---

The `file_complexity_history` table is **load-bearing for analysis**, not an optional perf-vs-cost knob. Per-(commit, file) indent statistics are how the downstream analyser reconstructs complexity *trajectories* (hotspot trends over time) — the snapshot `file_metrics` columns can't substitute, they're a single point.

**Why:** the user wants this signal and is willing to pay its (commits × eligible-files-per-commit) extraction cost. Removing it would hollow out a primary insight class even when the engagement would notionally accept a faster run.

**How to apply:** when proposing wall-clock-reduction levers, do NOT suggest a `file_complexity_history = false` opt-out, a sampling mode, or any narrowing of the (commit × file) coverage. Legitimate optimizations are *internal* to the existing extraction: cheaper blob scans, better batching of `git cat-file`, smarter exclusion regex tuning — anything that preserves full row coverage. The cost stays; only the cost-per-row may move.

Related: [[xray-one-shot-idempotent]] (cross-run caches are also off the table — the within-run cost is what we optimize).
