---
name: reference-github-bot-bypass-personal-repo
description: "GitHub personal-account repos cannot grant github-actions[bot] bypass for branch protection — Ruleset Integration bypass actors are org-only. Relevant whenever a release workflow's GITHUB_TOKEN needs to write to a protected branch."
metadata: 
  node_type: memory
  type: reference
  originSessionId: 604fbaf1-6d6f-4d09-a0ad-b90af659f621
---

On a **personal-account** GitHub repo, the workflow's `GITHUB_TOKEN` (authenticated as `github-actions[bot]`, GitHub App id `15368`) **cannot** be added as a bypass actor for branch protection. Confirmed two ways during the xray v0.4.0 release:

- Classic branch protection API rejected `bypass_pull_request_allowances` with `Validation Failed: Only organization repositories can have users and team restrictions` (422). Endpoint: `PUT /repos/{owner}/{repo}/branches/main/protection`.
- Rulesets API rejected `actor_type: "Integration"` with `Actor GitHub Actions integration must be part of the ruleset source or owner organization` (422). Endpoint: `POST /repos/{owner}/{repo}/rulesets`. The bot isn't an installed App on a personal account in the way Rulesets need.

The maintainer's own commits bypass BP via `enforce_admins: false` (or admin-bypass in Rulesets), but that's the *human* admin, not the bot.

**Workarounds when a release workflow needs to write to a protected branch:**

- **PAT** — fine-grained token owned by the admin, scoped `Contents: Read and write` on the one repo, expiry ~90d. Workflow reads it as a secret, e.g. `HOMEBREW_TAP_TOKEN`. Commits land as the admin → admin bypass applies. The conventional fix.
- **Out-of-band publish** — the workflow renders the content into its `dist/` (e.g. via goreleaser `skip_upload: true`); a separate skill/script run by the admin from their machine pushes it. xray uses this for `Casks/xray.rb` + `bucket/xray.json` via `scripts/publish-tap.sh` (see [[project-xray-baseline]]).
- **Dedicated unprotected branch** — `branch: dist-*` in goreleaser config so the bot pushes to e.g. `dist-homebrew` instead of `main`. Works for brew (`brew tap … --branch dist-homebrew`); breaks for Scoop (`bucket add` has no `--branch` flag).
- **Migrate the repo to an org** — fixes the constraint but is a bigger move than most one-developer projects warrant.

**Don't recommend:** disabling BP, lowering `enforce_admins`, force-pushing to dodge the check, or adding the bot via `restrictions.apps` (that field *limits* who can push, doesn't *grant* bypass).
