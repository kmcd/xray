---
name: feedback-release-tooling-smoke
description: "Run new release / publish / install scripts end-to-end against a real release artifact before considering them shipped. Static checks (shellcheck, goreleaser check) aren't enough — three real bugs in `scripts/publish-tap.sh` only surfaced on first live run."
metadata: 
  node_type: memory
  type: feedback
  originSessionId: 604fbaf1-6d6f-4d09-a0ad-b90af659f621
---

When building release-side tooling — installers, tap/bucket publishers, goreleaser stanzas, release workflows — static checks pass on bugs that real artifacts surface immediately.

**Why:** In the xray v0.4.0 → v0.4.1 work, `scripts/publish-tap.sh` was committed shellcheck-clean and prose-green. Running it against a real release found three real bugs in a row:

1. `git diff --quiet -- Casks/xray.rb` returned 0 when the file was untracked (diff ignores untracked); the idempotency short-circuit fired falsely. Fix: `git status --porcelain --` instead.
2. `git commit -- <new-path>` errored with "pathspec did not match any file(s)" — pathspec commits need the path already tracked. Fix: `git add --` before `git commit --`.
3. macOS Gatekeeper killed the Cask-installed binary with "Apple could not verify…" because the Cask had no `postflight` block stripping `com.apple.quarantine`. Fix: add the `postflight` to the Cask template; the binary is cosign-signed but not Apple-notarized.

Each fix required a separate commit + push because I only discovered the bug by trying to use the tool. Six commits where two would have done.

**How to apply:**

- For any release/install/publish script, before the first commit run it end-to-end against the actual artifact (`v0.X.Y`-tagged release, real package manager, real install command). If no real artifact exists yet, build one in a sandbox first (`goreleaser release --snapshot`, fake-publish to a scratch repo, etc.).
- For new Cask formulas / Scoop manifests, do the full `untap → tap → install → run` cycle on a fresh shell. `brew install` succeeding isn't sufficient — actually run the binary; Gatekeeper kills on first execution, not install.
- If you can't smoke end-to-end before commit (e.g., the test requires an unreleased tag), surface that gap explicitly in the handoff. Don't claim the tooling works.
- For longer-term: a `make snapshot-tap` target that runs goreleaser `--snapshot` + `scripts/publish-tap.sh` against the snapshot artifacts would let you pre-flight tap changes without cutting a real release. Worth considering if release-side iteration becomes frequent.
