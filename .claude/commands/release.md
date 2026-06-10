# Release

Cut a release end-to-end: roll the CHANGELOG, bump the README compatibility
table, commit, tag, push, wait for CI to ship binaries + SLSA + cosign, run
`/publish-tap` to land the brew Cask + Scoop manifest, smoke `brew install`
on this Mac, and report.

## Usage

`/release vX.Y.Z` — e.g. `/release v0.4.1`.

The version arg is required and must be a valid semver tag (`vMAJOR.MINOR.PATCH`,
optionally `-rcN` / `-betaN` suffix).

## Why this exists

The four-step ritual (`tag → push → wait → /publish-tap`) is correct but
forgets gracefully. After cutting v0.4.0 we hit two gaps: SLSA L3 regressed
silently when goreleaser exited non-zero on the bot-push step, and the Cask
template missed the macOS Gatekeeper postflight on the first attempt. Both
were caught in real time only because someone was watching. This skill bakes
in the watching.

## Step 1: Pre-flight

Abort loudly on any failure. No half-states.

```
[ -z "$(git status --porcelain)" ]                   # working tree clean
[ "$(git rev-parse --abbrev-ref HEAD)" = "main" ]    # on main
git fetch origin main && [ "$(git rev-parse main)" = "$(git rev-parse origin/main)" ]
make gates                                            # green
make prose                                            # green
```

Then validate the version arg:

- Matches `^v[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9]+)?$`.
- Strictly greater than `git describe --tags --abbrev=0` (semver compare).
- Not already a git tag (`git tag -l vX.Y.Z` empty).
- The `[Unreleased]` section of `CHANGELOG.md` has at least one bullet
  beneath at least one `### …` subsection. If empty, abort with: "nothing
  in `[Unreleased]` — what are we releasing?"

If any check fails, surface the situation and stop. Do not auto-fix.

## Step 2: Draft the intro paragraph

Read the bullets under `[Unreleased]` in `CHANGELOG.md`. Draft a one-sentence
theme paragraph in the same voice as the v0.3.0 / v0.4.0 intros (see
`CHANGELOG.md` around lines 39 and 42 of the v0.4.0 section as exemplars —
short, declarative, names the through-line of the entries).

Surface the draft via `AskUserQuestion` with three options:

- **Ship it (recommended)** — use the draft as-is.
- **Edit** — user replies with the revised paragraph; use that.
- **Abort** — stop the release.

Do not proceed until the user confirms.

## Step 3: CHANGELOG rollover

Edit `CHANGELOG.md`:

1. Replace `## [Unreleased]` with two lines:
   ```
   ## [Unreleased]

   ## [X.Y.Z] — YYYY-MM-DD
   ```
   where `YYYY-MM-DD` is today's date (UTC).
2. Insert the confirmed intro paragraph as a blank line then prose then blank
   line, immediately under the new `## [X.Y.Z] — …` heading.
3. Leave every `### …` subsection and bullet untouched — they now live under
   `[X.Y.Z]` rather than `[Unreleased]`.

Verify with `make prose` afterwards; abort and surface if it fails.

## Step 4: README updates

In `README.md`:

1. **Compatibility table** — add a new row at the current `schema_version`
   (read it from `internal/model/...` or the last row of the existing table).
   Format must match: `| X.Y.Z        | N              |`.
2. **Verifying-the-binary example** — bump `VERSION=...` to the new version.

Verify with `make prose`; abort if it fails.

## Step 5: Release commit

```
git commit -m "release: vX.Y.Z" -- CHANGELOG.md README.md
git push
```

Pathspec form per `CLAUDE.md` workflow. Push succeeds via admin bypass on
`main`.

## Step 6: Tag and push

```
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
```

Match the convention in the existing tag log (`git cat-file -p v0.3.0` shows
the shape).

## Step 7: Wait for CI

The tag push triggers `release.yml`. Capture its run id:

```
sleep 10
RUN_ID=$(gh run list --workflow=release.yml --branch=vX.Y.Z --limit=1 --json databaseId --jq '.[0].databaseId')
gh run watch "$RUN_ID" --exit-status
```

`--exit-status` makes the watch propagate the run's success / failure.

On failure:
- Run `gh run view "$RUN_ID" --log-failed | tail -50` and surface the failing
  step's tail.
- **Stop**. Do not proceed to publish-tap. The user needs to decide whether
  to fix-forward (`/release vX.Y.{Z+1}` after the fix) or recover the
  half-state manually.

On success: proceed.

## Step 8: Publish tap

```
scripts/publish-tap.sh vX.Y.Z
```

The script renders `Casks/xray.rb` + `bucket/xray.json` with the real
CI-built sha256s, pathspec-commits, and pushes. It's idempotent — re-running
when nothing changed exits 0 with a "no-op" message.

If the script errors, surface the failure and stop. The standalone
`/publish-tap vX.Y.Z` skill is the recovery path once the underlying issue
is fixed.

## Step 9: Smoke

Verify the published Cask actually installs and runs end-to-end:

```
brew untap kmcd/xray 2>/dev/null
brew uninstall --cask xray 2>/dev/null
brew tap kmcd/xray https://github.com/kmcd/xray
brew install kmcd/xray/xray
xray version
```

Assert the `xray version` output's version string matches `X.Y.Z`. On
mismatch (wrong version, Gatekeeper kill, install error): surface the full
brew output + the `xray version` exit code and stop. The release tag and
binaries are published — recovery is via `/publish-tap` after fixing the
Cask template, then re-running the smoke manually.

Scoop smoke is out of scope (no Windows VM wired up locally). The manifest
is byte-checkable against goreleaser's snapshot output if needed.

## Step 10: Report

Surface to the user:

- **Release page**: `https://github.com/kmcd/xray/releases/tag/vX.Y.Z`
- **Cask file**: `https://github.com/kmcd/xray/blob/main/Casks/xray.rb`
- **Scoop manifest**: `https://github.com/kmcd/xray/blob/main/bucket/xray.json`
- **Smoke output**: the `xray version` line, verbatim.
- **Wall-clock**: total time from `/release` invocation to here.

Do not offer follow-up tasks; end the message.

## Idempotency notes

Re-running `/release vX.Y.Z` after a partial failure picks up where it
stopped:

- CHANGELOG rollover: detect existing `## [X.Y.Z] — …` section; skip the
  edit if it's already there.
- Tag: `git tag -l vX.Y.Z` non-empty → skip tag creation.
- Tag push: `git ls-remote origin refs/tags/vX.Y.Z` non-empty → skip push.
- publish-tap: idempotent already (no-op when Cask matches).

The smoke step always re-runs. It's the only end-to-end check; it should
catch anything the upstream steps missed.

## When not to invoke

- `[Unreleased]` is empty (Step 1 catches this; mentioned here so you don't
  invoke pointlessly).
- You haven't run `/ready` on the most recent commit landed on `main`. The
  release is shipping that commit's binary; work that didn't pass through
  the completion gate shouldn't ride a tag.
- You're not at a keyboard for the next ~5 minutes. The intro confirmation
  and the smoke step both need a human present.
- CI or cron context. `/release` needs interactive judgement at two points.
