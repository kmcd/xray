---
name: feedback-schema-vs-semver
description: "For xray, schema_version (integer) is the downstream contract; semver tags are for the binary. Don't conflate them in discussions or in commits."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 334ab2b5-e8b4-4939-93e9-032dc922183e
---

xray has two version axes:

- **semver tags** (`v0.1.0`) — for the binary. Required by goreleaser / cosign / GitHub Releases. Soft contract pre-1.0; minor bumps may break the CLI surface.
- **`schema_version`** (integer in `manifest.json` and `_schema` table) — for the artifact. Hard contract. The downstream Ruby analyser refuses unknown values. Bumped only per CLAUDE.md rules: column removed/renamed, type changed, semantics changed, or `source` enum value dropped. Adding tables or nullable columns does **not** bump.

**Why:** The user asked "Do I need semver?" during v0.1.0 to test whether the framing was sound. The answer: yes for the binary (tooling expects it), but `schema_version` is the contract that gates whether an artifact is readable.

**How to apply:**
- When proposing a release, explicitly state both the next semver tag AND whether `schema_version` bumps. Default: it does not bump. Surface the bump only when a rule-named change motivates it.
- CHANGELOG.md entries lead with the **Schema** section so the analyser-affecting changes are visible without scrolling. Keep that ordering.
- In commit messages, schema-relevant changes are worth calling out explicitly (`add commits.signature_verified column (schema bump)`).
- Don't propose moving xray to date-based or sequential versioning; goreleaser and cosign infrastructure expect semver.
