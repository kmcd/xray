# Scheduled extraction with GitHub Actions

This guide walks through setting up a scheduled `xray` extraction using
a GitHub Actions workflow in the customer's repo. The workflow runs on
a cron schedule, produces an artifact, and uploads it to a
consultant-managed S3 bucket — or attaches it as a GitHub Actions
artifact. No consultant infrastructure, no GitHub App.

The template lives at
[`.github/workflows/xray-extract.yml.example`](../.github/workflows/xray-extract.yml.example).
Copy it, edit the teams block, and provision three secrets. A working
run takes under five minutes to set up.

## When to use this

Use this pattern when the engagement spans multiple quarters and you want
re-extractions to run without per-run engineer time. The cron fires on
schedule, the artifact lands in the handover channel, and you pick it up
on your end.

For a one-off extraction, the manual `xray run` path in
[docs/operator-setup.md](./operator-setup.md) is faster.

## Requirements

- A GitHub repository the customer controls (the one running the workflow,
  not necessarily the repos being extracted)
- A GitHub personal access token (PAT) with the scopes listed below
- Either an S3 bucket the consultant has set up (option A) or access to
  the repo's Actions UI (option B)

## Step 1: Copy the workflow file

Copy `.github/workflows/xray-extract.yml.example` to
`.github/workflows/xray-extract.yml` in the customer's repo.

```bash
cp .github/workflows/xray-extract.yml.example .github/workflows/xray-extract.yml
git add .github/workflows/xray-extract.yml
git commit -m "add xray scheduled extraction workflow"
git push
```

GitHub Actions picks up the workflow as soon as the file lands on the
default branch.

## Step 2: Provision the PAT

Create a GitHub PAT with the following scopes. The scopes match the
manual `xray run` path — nothing additional is needed for the scheduled
run.

**Classic PAT** (Settings → Developer settings → Personal access tokens → Tokens (classic)):

| Scope | Why |
|-------|-----|
| `repo` | Read access to private repositories (commits, PRs, reviews, branch protection) |
| `read:org` | Read org membership and team structure |

**Fine-grained PAT** (Settings → Developer settings → Personal access tokens → Fine-grained tokens):

Set "Repository access" to the repos being extracted. Grant:

| Permission | Level |
|------------|-------|
| Contents | Read |
| Pull requests | Read |
| Metadata | Read (always required) |

Fine-grained tokens with `Read` permissions give `xray` exactly what it
needs and nothing more. Prefer them over classic PATs when your GitHub
plan supports them.

The PAT stays on the customer's machine — or in the repo's encrypted
secrets store. It is never sent to the consultant and never appears in the
artifact. See [docs/security.md → No secrets in the artifact](./security.md#3-no-secrets-in-the-artifact).

## Step 3: Add secrets and variables

In the customer's repo, go to Settings → Secrets and variables → Actions.

### Required secret

| Name | Value |
|------|-------|
| `XRAY_GH_TOKEN` | The PAT from step 2 |

### Option A: S3 handover (consultant-managed bucket)

The consultant provides a bucket name and a write-only IAM key pair.

| Name | Type | Value |
|------|------|-------|
| `CONSULTANT_BUCKET_KEY_ID` | Secret | IAM access key ID |
| `CONSULTANT_BUCKET_KEY_SECRET` | Secret | IAM secret access key |
| `CONSULTANT_BUCKET` | Variable | Bucket name (e.g. `acme-xray-drop`) |
| `CONSULTANT_BUCKET_REGION` | Variable | AWS region (e.g. `us-east-1`); defaults to `us-east-1` if absent |

The IAM key should have only `s3:PutObject` on the target bucket prefix.
The consultant sets this up on their end. If the consultant has not
provided these yet, comment out the S3 step in the workflow and use
option B in the meantime.

### Option B: GitHub Actions artifact

No extra secrets or variables. The artifact appears under the workflow
run in the repo's Actions UI. The consultant downloads it directly.

In the workflow file, remove the S3 step and uncomment
`upload-artifact` block.

Artifacts expire after the `retention-days` value (default 90 in the
template). The consultant must download before they expire.

## Step 4: Edit the config block

In the `prepare config` step, update the `[teams]` block with the repos
to extract:

```toml
[teams]
engineering = [
  "your-org/repo-a",
  "your-org/repo-b",
]
```

Team names are arbitrary strings. Each value is an `owner/repo` slug.
A repo may appear in only one team. Full config reference:
[docs/spec.md → Configuration](./spec.md).

The window starts at `2021-01-01` and ends at today (set at runtime by
the workflow). This captures the pre-AI baseline through the current run
date. To narrow it, change the start date in the `cat > config.toml`
block.

## Step 5: Trigger manually to validate

Before relying on the cron, trigger the workflow by hand:

Actions → xray scheduled extraction → Run workflow → Run workflow

Watch the run log. A successful run ends with a `run: artifact …
sha256=<hex>` line and exit code `0` (clean) or `2` (partial — artifact
produced, connector errors recorded in manifest).

If the run exits with code `1` (config/preflight error), check:
- The PAT secret is set and has the required scopes
- The repos in the `[teams]` block exist and the PAT can read them
- The S3 variables are set (option A only)

After a clean manual run, the scheduled cron takes over.

## Schedule tuning

The default cron `0 2 1 */3 *` fires at 02:00 UTC on the first of
January, April, July, and October. To change the cadence, edit the
`cron:` value in the workflow file using standard five-field syntax:

| Field | Values |
|-------|--------|
| Minute | 0–59 |
| Hour | 0–23 (UTC) |
| Day of month | 1–31 |
| Month | 1–12, or `*/N` for every N months |
| Day of week | 0–7 (0 and 7 = Sunday) |

Examples:

```yaml
# Monthly on the 1st
- cron: '0 2 1 * *'

# First of every month in Q1 and Q3
- cron: '0 2 1 1,7 *'
```

GitHub Actions does not guarantee exact firing times for scheduled
workflows. Runs may be delayed by up to an hour under heavy platform
load.

## Disabling and re-enabling the schedule

To pause the schedule without editing the workflow file:

1. Settings → Secrets and variables → Actions → Variables → New repository variable
2. Name: `XRAY_ENABLED`, Value: `false`

The next scheduled run skips the job. The workflow still appears in the
Actions UI; it immediately exits as skipped.

To re-enable, delete the variable or set it to any value other than `false`.

Manual `workflow_dispatch` triggers are not affected by `XRAY_ENABLED` — they
run unconditionally. Add `if: vars.XRAY_ENABLED != 'false'` to the
`workflow_dispatch` block if you want the variable to suppress manual runs too.

## Cost

GitHub-hosted runners bill at approximately $0.008/minute for
`ubuntu-latest`. A 12-hour cap costs at most ~$5.76 per run. Full-window
extractions against mid-sized organisations (10–30 repos, 3–5 years of history)
typically complete in 30–90 minutes — ~$0.24–$0.72 per quarterly run.

Costs are charged to the account that owns the repo running the workflow,
not the org being extracted.

## Self-hosted runners

To run on the customer's own infrastructure, change `runs-on:` from
`ubuntu-latest` to the label of their self-hosted runner:

```yaml
runs-on: [self-hosted, linux, x64]
```

The runner needs:
- `git` on `PATH` (for repo clones during extraction)
- `gh` CLI on `PATH` (for release download in the install step), or
  replace the install step with a direct `curl` to a pinned release archive
- `aws` CLI on `PATH` (option A only)
- Outbound HTTPS to `github.com` and configured connector endpoints

No inbound ports, no callbacks. See
[docs/spec.md](./spec.md) for the full connector network surface.

## PAT rotation

The stored PAT should be rotated at least every 90 days, matching GitHub's
recommended maximum lifetime for classic tokens. When the token expires,
the next extraction fails at the `run extraction` step with a `401`
response recorded in the manifest — a predictable, visible failure mode.

To rotate:
1. Create a new PAT with the same scopes (step 2 above)
2. Update the `XRAY_GH_TOKEN` secret with the new value
3. Trigger a manual run to confirm the new token works
4. Revoke the old PAT

Set a calendar reminder at PAT creation time for 80 days out (10 days
before the 90-day mark) to avoid a missed extraction.

At engagement close, revoke the PAT regardless of its remaining
lifetime. See [docs/engagement-guide.md → End of engagement](./engagement-guide.md#7-end-of-engagement).

## Verifying the artifact before sending

The run produces two files: the `.tar.gz` artifact and a sibling `.log`
file. Both are uploaded to S3 (or attached as separate artifacts under
option B — add a second `upload-artifact` step for the `.log` file).

The `sha256=` line at the end of the run log is the artifact checksum.
Send it alongside the `.tar.gz` so the consultant can verify receipt
without a separate checksum file. See
[docs/engagement-guide.md → Receiving the artifact](./engagement-guide.md#1-receiving-the-artifact).

## See also

- [`.github/workflows/xray-extract.yml.example`](../.github/workflows/xray-extract.yml.example) — the workflow template
- [docs/operator-setup.md](./operator-setup.md) — manual extraction walkthrough
- [docs/engagement-guide.md](./engagement-guide.md) — what the consultant does with the artifact
- [docs/security.md](./security.md) — what the artifact contains and what the binary guarantees
- [docs/spec.md](./spec.md) — full config reference and command surface
