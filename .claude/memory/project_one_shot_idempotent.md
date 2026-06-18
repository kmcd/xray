---
name: xray-one-shot-idempotent
description: "xray runs are one-shot and idempotent; cross-run clone reuse / persistent caches are off the design table — don't propose them as perf levers."
metadata: 
  node_type: memory
  type: project
  originSessionId: a741dacc-c9e3-4034-a491-da9423950a2d
---

xray runs are one-shot and idempotent by design — each invocation is a full self-contained extract. Cross-run state (cached clones, incremental indexes, resumable runs) is not on the design table.

**Why:** the ideal path is `xray run` → `.tar.gz` with nothing carried over from a prior run. Preserves the security-review story (single binary, single config, reproducible artifact) and the consultant-handoff workflow (one client run, one artifact, no shared state). This is stronger than the CLAUDE.md "no incremental extraction" non-goal — that one excludes resumable runs; this memory adds clone reuse and any other cross-run cache.

**How to apply:** when proposing perf levers for big-repo wall-clock, do NOT suggest reusing clones, persisting `.git` directories, caching enriched commits, or otherwise carrying state between runs. Legitimate levers stay *within a single run*: parallelize the walk, skip-read on excluded paths, opt-out flags for expensive tables, batch API calls, prune over-fetched data. Cross-run wins are out of scope.

Related: [[xray-v0.4.1-baseline]] (the v1 settled-context shape), CLAUDE.md "Non-goals (v1)" ("No incremental extraction. Each run is full within the window.").
