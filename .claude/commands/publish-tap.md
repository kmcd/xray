# Publish tap

Land `Formula/xray.rb` (brew Formula) and `bucket/xray.json` (Scoop manifest)
on `main` for a release tag. Run after a tagged release's CI pipeline has
shipped the binaries to GitHub Releases.

`.goreleaser.yaml` declares `skip_upload: true` on `brews` and `scoops`, so
the CI release workflow no longer attempts to push the tap files itself
(which fails against `main` branch protection — `github-actions[bot]` isn't
an admin and personal-account Rulesets can't add it as a bypass actor).
Instead, this skill takes over after the release: it reads the real CI-built
sha256s from the published `checksums.txt`, renders both files locally, and
commits them under the user's admin identity (which bypasses BP).

Two modes:

- **`/publish-tap`** (no arg) — target the most recent git tag.
- **`/publish-tap <tag>`** — target the given tag (e.g., `v0.4.1`). Use this to
  back-publish a release whose tap files never landed, or to re-run after
  fixing a script bug.

## Step 1: Pre-conditions

- Working tree clean.
- Current branch is `main`.
- The target tag has a GitHub release with a `checksums.txt` asset.

The script (`scripts/publish-tap.sh`) checks these and aborts loudly on
mismatch. If you hit a guard, surface the situation and stop — do not paper
over a dirty working tree.

## Step 2: Run

```
scripts/publish-tap.sh [<tag>]
```

The script:

1. Resolves the target tag (arg or `git describe --tags --abbrev=0`).
2. Downloads `checksums.txt` from the GitHub release to a `mktemp -d`.
3. Parses the four macOS/Linux sha256s and the one Windows sha256.
4. Renders `Formula/xray.rb` and `bucket/xray.json` (templates inlined in
   the script — the `homepage`, `desc`, and `license` strings must stay in
   sync with the `brews:` and `scoops:` stanzas in `.goreleaser.yaml`).
5. Pathspec-commits and pushes to `main`. Commit subject:
   `release: <tag> brew Formula + Scoop manifest`.

If the rendered files already match the working tree (e.g., you ran the script
a second time for the same tag, or hand-edited it earlier), the script exits 0
with a "nothing to commit" message — that is a no-op, not a failure.

## Step 3: Smoke

Re-tap and reinstall on this Mac to verify the published Formula actually
resolves:

```
brew untap kmcd/xray 2>/dev/null || true
brew uninstall xray 2>/dev/null || true
brew tap kmcd/xray https://github.com/kmcd/xray
brew install kmcd/xray/xray
xray version
```

Assert that `xray version` prints the target tag's version. If anything errors,
stop — do not declare success.

For Windows / Scoop the equivalent smoke is `scoop install xray` in a Windows
VM, which we don't have wired up locally. Skip the Scoop smoke unless a
Windows customer reports a problem; the manifest is byte-checkable against
GoReleaser's snapshot output if needed.

## Step 4: Report

Surface to the user:

- Release page: `https://github.com/kmcd/xray/releases/tag/<tag>`
- Formula file: `https://github.com/kmcd/xray/blob/main/Formula/xray.rb`
- Scoop manifest: `https://github.com/kmcd/xray/blob/main/bucket/xray.json`
- The `xray version` output line from the smoke (proves install worked
  end-to-end).

Do not offer follow-up tasks; end the message.
