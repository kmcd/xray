This document is the operator-side setup guide for `xray`. Audience: the
customer's engineer who installs the binary, configures connectors, and
runs the extraction. No prior `xray` experience required.

The counterpart document — what happens to the artifact once it leaves
your environment — is [docs/engagement-guide.md](./engagement-guide.md).
Token scope details, network surface, and the artifact's security
guarantees are in [docs/security.md](./security.md).

## 1. Install

### macOS

```bash
brew tap kmcd/xray https://github.com/kmcd/xray
brew install kmcd/xray/xray
```

### Linux

```bash
curl -sSfL https://raw.githubusercontent.com/kmcd/xray/main/scripts/install.sh | sh
```

The script detects OS and architecture, downloads the latest release
archive, verifies its sha256 against `checksums.txt`, and installs to
`/usr/local/bin/xray`. Set `XRAY_INSTALL_DIR=$HOME/.local/bin` to install
without `sudo`. Set `XRAY_VERIFY_COSIGN=1` to additionally verify the
cosign signature (requires `cosign` on `PATH`).

### Windows

```powershell
scoop bucket add xray https://github.com/kmcd/xray
scoop install xray
```

### Verify

```bash
xray version
```

The output includes the tool version, commit, and build date. Keep the
`xray version` string — the consultant needs it to verify the binary
you ran and map it to a schema version.

For corporate proxy, custom CA, or allowlist firewall configuration, see
[docs/enterprise.md](./enterprise.md) before proceeding.

## 2. Set token environment variables

`xray init --probe` reads credentials from environment variables. Set the
ones for each connector you want to probe before running the command.
Unset connectors are skipped; you can add their tokens later by editing
the draft config.

| Connector | Env var | Token type |
|-----------|---------|------------|
| GitHub | `GITHUB_TOKEN` | Personal access token or GitHub App with `repo` and `read:org` |
| CircleCI | `CIRCLECI_TOKEN` | CircleCI v1.1 personal token |
| Bugsnag | `BUGSNAG_AUTH_TOKEN` | Bugsnag auth token |
| Honeycomb | `HC_API_KEY` or `HONEYCOMB_API_KEY` | Honeycomb configuration key |
| Sentry | `SENTRY_AUTH_TOKEN` | Sentry auth token with `project:read` |

`xray` is read-only. Tokens are never logged and never leave your machine;
see [docs/security.md → No secrets in the artifact](./security.md#3-no-secrets-in-the-artifact).

```bash
export GITHUB_TOKEN="ghp_..."
export CIRCLECI_TOKEN="..."
export BUGSNAG_AUTH_TOKEN="..."
export HC_API_KEY="..."
# export SENTRY_AUTH_TOKEN="..."  # add when available
```

### Creating a GitHub personal access token

**Fine-grained tokens (recommended):** GitHub Settings → Developer settings →
Personal access tokens → Fine-grained tokens → Generate new token.

Required repository permissions:

| Permission | Access |
|------------|--------|
| Contents | Read |
| Metadata | Read |
| Pull requests | Read |
| Actions | Read |

Required organisation permissions:

| Permission | Access |
|------------|--------|
| Members | Read |

Set the expiry to 7 days for a one-shot extraction. Fine-grained tokens support
per-repo access restrictions, so scope them to the repos being extracted if your
org requires it.

**Org-restricted PATs.** Some organisations require admin approval before a PAT
can access private resources. If `xray check` returns 403 on repo endpoints,
ask your GitHub org admin to approve the token under GitHub → Organisation
settings → Personal access tokens → Pending requests.

**Classic tokens:** If fine-grained tokens are not available for your org, use
a Classic token with `repo` and `read:org` scopes.

## 3. Run the probe and review the report

`xray init --probe` hits each configured connector, discovers live state,
prints a summary, and writes a draft config to `xray.toml`.

```bash
xray init --probe --org acme-corp --out xray.toml
```

### Sample probe output

The output below is from a representative run against a mid-size org. Each
block corresponds to one connector; annotations follow the listing.

```
Probing GitHub...
  token scopes: repo, read:org
  47 repos total; 12 active (not archived, pushed in last 90d)

Probing CircleCI...
  8 followed projects for this org
  mapping convention: gh/<org>/<repo> → <org>/<repo>

Probing Bugsnag...
  org: Acme Corp
  5 projects found
  suggested repo mappings:
    api                              → acme-corp/api [high]
    data-pipeline                    → acme-corp/pipeline [high]
    web-app                          → acme-corp/web [medium]
  projects with no repo match (fill in manually):
    legacy-ios
    android-app

Probing Honeycomb...
  3 datasets found
  datasets with deploy markers linking to acme-corp GitHub commits:
    production: 142 markers covering 2 repos
      acme-corp/api  118 markers
      acme-corp/web  24 markers
    staging: 31 markers covering 1 repos
      acme-corp/api  31 markers
  repos with no markers in any dataset:
    acme-corp/data-pipeline  ← CI pipeline may not post markers
    acme-corp/workers  ← CI pipeline may not post markers

Skipping Sentry (SENTRY_AUTH_TOKEN not set)

Wrote xray.toml — review before running `xray run`.
```

### Reading the output

**GitHub — active repos.** The probe counts repos not archived and pushed
within the last 90 days. 47 total repos but 12 active is normal for an
org that has accumulated historical or forked repos over time; the 12
active repos go into the draft config's `[teams]` block.

**CircleCI — mapping convention.** CircleCI identifies pipelines by
`gh/<org>/<repo>` slugs. The probe lists each followed project and records
the mapping convention in the draft config. If you have CircleCI pipelines
under a different VCS prefix (Bitbucket, GitLab), those are excluded —
`xray` extracts GitHub-backed CircleCI projects only.

**Bugsnag / Sentry — confidence levels.** The probe fuzzy-matches project
names against GitHub repo slugs. `[high]` means the normalised names match
exactly. `[medium]` means a substring match — review these before running.
Projects with no repo match are listed separately; fill in their repo
mappings manually in the draft config or delete the line to skip them.

**Honeycomb — dataset recommendation.** The probe walks markers across
every dataset and records which ones contain deploy events pointing at
your org's GitHub commits. The dataset with the most markers becomes the
`dataset` value in the draft config. Repos with no markers in any dataset
are flagged: their CI pipelines likely do not post Honeycomb deploy
markers, so `xray` will find no deploy signal for them from this
connector. That is expected; it is not a token-scope problem.

**Skipped connectors.** Any connector whose token env var is not set is
skipped. The draft config still includes a placeholder block for it with
an empty token field. Set the token and re-run `xray init --probe` (with
`--force` to overwrite), or edit the draft by hand.

## 4. Edit the draft config

Open `xray.toml` in an editor. The minimum edits before running:

1. **Review the window.** The scaffold sets `window = "2021-01-01..<today>"`. The 2021
   start captures the pre-AI baseline (~18 months before Copilot GA in June 2022).
   On a large repo, narrow the window for a first run to validate connectors before
   widening to the full range.

   ```toml
   window = "2021-01-01..2026-06-12"
   ```

2. **Paste tokens.** Each connector block has `token = ""`. Paste the
   actual token values — or set them as env vars and leave the field
   empty (the connector will read from the environment at run time).

3. **Verify connector mappings.** Check each `[connectors.X.projects]`
   block. High-confidence matches are usually correct. Medium-confidence
   matches may need adjustment. Remove or comment out lines for projects
   that do not map to a repo you want to extract.

4. **Split teams.** The draft puts all active repos under
   `[teams] unassigned`. If the engagement covers multiple teams, split
   the list into named team groupings:

   ```toml
   [teams]
   platform = [
       "acme-corp/api",
       "acme-corp/workers",
   ]
   product = [
       "acme-corp/web",
       "acme-corp/mobile",
   ]
   ```

   Team names appear in the manifest; the analyser uses them for
   cross-team comparisons. Repos that genuinely do not belong to a named
   team can stay in `unassigned`.

5. **Remove unneeded connector blocks.** If a connector is not relevant
   to this engagement, delete its `[connectors.X]` section. An empty
   token field causes `xray check` to fail; remove the block rather than
   leaving it blank.

The config file stays on your machine. Do not commit it to git and do not
send it to the consultant — it contains your tokens.

## 5. Validate the config

```bash
xray check
```

`xray check` runs in two stages. First, an offline config parse and
schema validation. Second, a live preflight against each configured
connector.

Sample output from a correctly configured run:

```
ok  config valid
ok  git              on PATH

read-only contract
github           token scopes: repo, read:org
               xray will call only read endpoints (assertion: no
               Create/Update/Delete/Add/Remove methods invoked)
ok  github_actions   authenticated (read-only)
ok  circleci         authenticated (read-only)
ok  bugsnag          authenticated (read-only)
ok  honeycomb        authenticated (read-only)

clone access
ok  acme-corp/api    clone access ok
ok  acme-corp/web    clone access ok
ok  acme-corp/pipeline  clone access ok

Plan
  repos:      12 across 3 teams
  window:     2025-01-01..2025-03-31 (89 days)
  connectors: github, github_actions, circleci, bugsnag, honeycomb
  estimated:  ~2.1 GiB clone, ~8.4k API calls, ~18 min wall-clock

permission-gated endpoints
  acme-corp/api        branch_protection    inaccessible (403)
  acme-corp/web        branch_protection    inaccessible (403)
```

**Common failures and fixes:**

- `FAIL github: HTTP 401` — token is invalid or expired. Check the token
  value and its expiry date in GitHub.
- `FAIL git: could not read Username for 'https://github.com'` — git
  lacks HTTPS credentials. Run `gh auth setup-git` or configure a
  credential helper.
- `FAIL acme-corp/foo  exit status 128` — the token lacks read access to
  that repo. Either the token does not have `repo` scope or the repo is
  in a different org.
- TLS certificate errors on a corporate network — see
  [docs/enterprise.md](./enterprise.md) for CA and proxy setup.

**Permission-gated endpoints** are informational, not errors. A `403` on
`branch_protection` means the token lacks admin scope on that repo; `xray`
records the absence in `extraction_provenance` as `accessible: false`.
The consultant reads this as *unknown*, not *not configured* — see
[docs/engagement-guide.md → 3. Reading the manifest](./engagement-guide.md#3-reading-the-manifest).
If you want branch protection data, re-run with a token that has `admin:read`
on those repos.

Once `xray check` exits 0 with no FAIL lines, the config is ready.

## 6. Run the extraction

```bash
xray run
```

A live (repo × connector) status grid renders on the terminal. Extraction
typically takes 10–30 minutes depending on org size, window length, and
connector rate limits.

```
xray run · elapsed 04:12 · ETA 14:47 ±2m · 3/4 workers

repo                clone           github           gh_actions       bugsnag
acme-corp/api       ✔ done          ✔ 4213 rows      ✔ 618 rows       ✔ 88 rows
acme-corp/web       ✔ done          ● prs             ▢ pending        ▢ pending
acme-corp/pipeline  ● clone         ▢ pending         ▢ pending        ▢ pending
```

On a long run (1+ hour), rate-limit waits appear as `rate limited, waiting Ns`,
`primary limit low, waiting Ns`, or `adaptive pacing, waiting Ns` lines — these
are normal and the run resumes automatically. Run under `tmux` or `screen` to
keep the process alive if you need to close your terminal.

On completion, `xray run` writes two files to the current directory:

```
xray-export-20250215T143022Z.tar.gz   # the artifact
xray-export-20250215T143022Z.log      # mirror of stderr
```

The last line of the log records the artifact path and its SHA-256:

```
run: artifact xray-export-20250215T143022Z.tar.gz sha256=3a4b1c...
```

Send the consultant both files and the `xray version` output from the binary
you ran — they need it to verify provenance and map the run to a schema version.
Do not send the config file — it contains your tokens.

Common handover channels are Slack DM, a download link your consultant
provides, or a private file share. The artifact is typically 10–200 MiB for a
multi-repo engagement.

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | Clean run; artifact is complete |
| `1` | Config or pre-flight error; no artifact written |
| `2` | Partial run; artifact written, connector errors recorded in manifest |
| `3` | Fatal error; no artifact written |
| `130` | Second Ctrl-C force exit; artifact may be incomplete |

Exit code `2` is not a failure to retry immediately. Open the `.log` file,
find the connector error lines, address the underlying cause (token scope,
rate-limit budget, provider downtime), and re-run.

## See also

- [docs/security.md](./security.md) — what the artifact contains, what
  stays on your machine, and the read-only guarantee.
- [docs/enterprise.md](./enterprise.md) — corporate proxy, custom CA,
  firewall configuration.
- [docs/threat-model.md](./threat-model.md) — trust boundaries and what
  happens if the artifact is leaked.
- [README.md → Verifying the binary](../README.md#verifying-the-binary)
  — cosign verification for security-team installs.
