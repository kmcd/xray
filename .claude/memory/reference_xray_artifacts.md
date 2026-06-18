---
name: reference-xray-artifacts
description: "Where xray's load-bearing documents live — spec, ADR, schema, changelog, agentic infra. Quick orient for sessions that start with no context."
metadata: 
  node_type: memory
  type: reference
  originSessionId: 334ab2b5-e8b4-4939-93e9-032dc922183e
---

Within the xray repo (working dir `/Users/kmcd/src/xray`):

- `CLAUDE.md` — the canonical spec. Don't summarise from memory; re-read when a question touches behaviour, schema, or non-goals.
- `CHANGELOG.md` — per-release notes ordered Schema → Connectors → Behaviour.
- `README.md` — install + cosign verify + compatibility table.
- `docs/schema.md` — full reference for the SQLite artifact's tables.
- `tmp/adr.md` — running architecture decisions log (numbered, append-only). Not committed to history snapshots — local working doc.
- `.claude/commands/ready.md` — the `/ready` completion gate.
- `.claude/diff_review.md` — auto-applied review criteria for any diff.
- `.claude/agent_prompt_template.md` — verbatim clauses for dispatching parallel agents.

Outside the repo:

- GitHub: https://github.com/kmcd/xray (private). Issues numbered 1–42 cover the M0–M11 build; all closed at v0.1.0.
- Releases: https://github.com/kmcd/xray/releases — cosign-signed checksums + per-platform binaries.

**How to apply:** When a session starts cold (e.g. "look at the diff" or "is this a schema break?"), open `CLAUDE.md` and the relevant section of `docs/schema.md` first. Don't infer schema rules from the model code — the spec is authoritative and `tmp/adr.md` records *why* decisions diverged from defaults.
