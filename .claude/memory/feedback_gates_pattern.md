---
name: feedback-gates-pattern
description: "The user adopted gauge_intelligence's gate discipline for xray — `make gates` locally, `bin/ship` before push, `/ready` as the completion-gate command. Don't push without running them."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 334ab2b5-e8b4-4939-93e9-032dc922183e
---

For xray (and Go projects with the same shape), the gate hierarchy is:

- `make gates` — the runnable target. Wraps `lint`, `vuln` (govulncheck), and `coverage` (go-test-coverage). Same three jobs CI runs on push.
- `bin/ship` — thin wrapper over `make gates`; the conventional pre-push entry point.
- `/ready` (`.claude/commands/ready.md`) — the completion-gate slash command. Runs gates, walks `.claude/diff_review.md`, invokes `code-review`, then forces a scope sweep before declaring work done.

**Why:** Adopted from `gauge_intelligence`'s rubocop/undercover/brakeman/bundler-audit pattern. The discipline earns its keep — catching a CVE or gosec finding pre-push is cheap; doing it after a tagged release is not.

**How to apply:**
- **Before `gh issue close`, always run `/ready`.** Not "at minimum `make gates`" — full `/ready`. The smoke step (Step 6) is the gate that catches perf-claim regressions and runtime row-volume drift that unit tests don't see. See [[feedback_close_after_smoke]].
- **Smoke targets:** `/ready` smoke uses `goreleaser/chglog` (small, fast, ~10s). `posthog/posthog` is for performance tests only — do not use it in `/ready`. Do not use the `xray` repo itself as a smoke target.
- When extending the project (new Go projects with a similar shape), default to the same trio: `make gates` target, `bin/ship` wrapper, `/ready` command.
- If a gate fails: fix it. Don't disable a gate to make the failure go away — that's the failure mode this discipline exists to prevent.
- Coverage thresholds in `.testcoverage.yml` are permissive at v0.x; tighten once the connector test surface stabilises. See [[project-xray-baseline]].
