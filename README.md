# xray

[![Build](https://github.com/kmcd/xray/actions/workflows/ci.yml/badge.svg)](https://github.com/kmcd/xray/actions/workflows/ci.yml)
[![CodeQL](https://github.com/kmcd/xray/actions/workflows/codeql.yml/badge.svg)](https://github.com/kmcd/xray/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/kmcd/xray/badge)](https://securityscorecards.dev/viewer/?uri=github.com/kmcd/xray)
[![SLSA 3](https://slsa.dev/images/gh-badge-level3.svg)](https://slsa.dev)
[![govulncheck](https://img.shields.io/badge/govulncheck-clean-success)](#)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](./LICENSE)

**xray** is a read-only extractor that produces a portable engineering-metrics artifact from a client's git, GitHub, CI, and error-tracker systems.

- **Captures**: commits, PRs, reviews, CI runs, deploys, incidents — structural data, no source content.
- **Produces**: a single `.tar.gz` (SQLite + JSON manifest) — verifiable SHA256, no secrets.
- **Touches**: GitHub, GitHub Actions, CircleCI, Sentry, Bugsnag, Honeycomb — read-only, even when tokens hold write scope.
- **Doesn't do**: source-content capture, per-individual rankings, daemon mode, scheduled runs.

> [!IMPORTANT]
> `xray` runs inside a customer environment against the customer's own
> credentials. The artifact contains no source code and no secrets.
> Security review: [docs/security.md](docs/security.md) ·
> [docs/threat-model.md](docs/threat-model.md). Vulnerability
> disclosure: [SECURITY.md](SECURITY.md).

## Trust

`xray` is intended to be run inside the customer's environment by the
customer's own operator, against the customer's own credentials, and the
artifact it produces is meant to survive a security review. The four
documents below describe what the binary does, what it cannot do, and
what a representative run actually looks like.

- [`docs/security.md`](docs/security.md) — what is captured, what is
  not, and the guarantees the binary makes (read-only, no source
  content, no secrets in the artifact, team-level only, logs).
- [`docs/threat-model.md`](docs/threat-model.md) — one-page trust
  boundaries, attack surface, malicious-binary and leaked-artifact
  analysis.
- [`docs/sample-manifest.json`](docs/sample-manifest.json) — a real
  `manifest.json` from a clean single-repo run against
  `goreleaser/chglog`, the same target `/ready` uses for smoke. Real
  row counts, real `extraction_provenance` block. Failure-mode
  endpoint states are documented in
  [`docs/security.md`](docs/security.md#7-failure-modes-for-security-review)
  rather than reproduced here.
- [`docs/sample-run.log`](docs/sample-run.log) — the matching `.log`
  file demonstrating no tokens, per-phase logs, and the post-run
  artifact summary.
- [`docs/engagement-guide.md`](docs/engagement-guide.md) — the
  consultant-side counterpart: what happens to the artifact after
  you send it. Public so the methodology stays auditable.

## Install

Download a release binary for your platform:

```bash
VERSION=0.3.0
OS=$(uname -s | tr A-Z a-z)                              # linux | darwin
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')

curl -L "https://github.com/kmcd/xray/releases/download/v${VERSION}/xray_${VERSION}_${OS}_${ARCH}.tar.gz" | tar -xz
sudo mv xray /usr/local/bin/
```

Or, for Go developers:

```bash
go install github.com/kmcd/xray/cmd/xray@latest
```

For production deployments, verify the cosign signature on the archive
before installing — see [Verifying the binary](#verifying-the-binary).

## Usage

The default flow: **configure → validate → run → export**.

### 1. Configure

```bash
# Generate a starter config interactively.
xray init

# Or scaffold from a GitHub org's repos.
xray init --org my-org --token "$GITHUB_TOKEN"
```

Hand-edit `xray.toml`: connector tokens, project mappings, team layout.
The file stays on the operator's machine — never committed to git, never
shared back to the consultant.

### 2. Validate and check

```bash
# Syntactic + schema check, offline. Defaults to ./xray.toml; pass an
# explicit path to point at a different config.
xray validate

# Live preflight against configured connectors (auth, rate limits, scope).
xray check
```

### 3. Run

```bash
# Full extraction. Produces ./xray-export-<UTC-timestamp>.tar.gz with a
# sibling .log file mirroring stderr (suppress with --no-run-log). See
# the sample-run block below the code for live TTY output.
xray run

# Machine-readable output. One NDJSON event per progress tick, terminated
# by a {"kind":"run_summary",...} object on stdout. See docs/spec.md.
xray run --output json | jq .

# Quiet success: only the artifact path is written to stdout.
xray run --output quiet
```

<details>
<summary>Sample run (live TTY output)</summary>

On a TTY, the default (`--output auto`) renders a live (repo × connector) status grid:

```
xray run · elapsed 04:12 · ETA 14:47 ±2m · 3/4 workers

repo                clone           github           gh_actions       sentry
kmcd/foo            ✔ done          ✔ 4213 rows      ● gh_actions     ✔ 312 rows
kmcd/bar            ✔ done          ● prs            ▢ pending        🔒 inaccessible
kmcd/baz            ● clone         ▢ pending        ▢ pending        ▢ pending
```

Non-TTY (CI, pipe to file) falls back to a stderr log with one line per phase boundary; force the log explicitly with `--output log`.

</details>

Exit codes: `0` clean, `1` config / pre-flight error, `2` partial run
(artifact produced, connector error recorded in manifest), `3` fatal.
See [`docs/spec.md`](./docs/spec.md) → `xray run` → "Exit codes".

### 4. Export

The run produces two files in the working directory:

- `xray-export-<UTC-timestamp>.tar.gz` — the artifact (SQLite + JSON manifest)
- `xray-export-<UTC-timestamp>.log` — the run log (mirrors stderr)

Inspect `manifest.json` inside the archive before sending — it lists every
connector's status, row counts, and per-endpoint errors. No source content,
no secrets. Sample: [`docs/sample-manifest.json`](docs/sample-manifest.json).

The full configuration reference and behaviour spec live in [`docs/spec.md`](./docs/spec.md). The output schema is documented in [`docs/schema.md`](./docs/schema.md). Agent-facing constraints (invariants, non-goals, schema-versioning rules) live in [`CLAUDE.md`](./CLAUDE.md).

## Compatibility

Pre-1.0, the schema is unstable; minor version bumps may introduce breaking schema changes. The analyser refuses to load artifacts at an unknown `schema_version`. Per-release changes that affect downstream consumers are tracked in [`CHANGELOG.md`](./CHANGELOG.md).

| xray version | schema_version |
| ------------ | -------------- |
| 0.1.0        | 1              |
| 0.2.0        | 1              |
| 0.2.1        | 1              |
| 0.2.2        | 1              |
| 0.3.0        | 2              |

`0.3.0` is the first release at `schema_version = 2`. Analysers pinned to `schema_version = 1` will refuse to load `0.3.0+` artifacts — see the [CHANGELOG](./CHANGELOG.md#030--2026-06-08) for the author-handle semantics shift driving the bump.

## Verifying the binary

Verify the cosign signature on `checksums.txt`, then verify the archive
against the checksum.

```bash
VERSION=0.3.0
OS=linux           # or darwin, windows
ARCH=amd64         # or arm64 (not available on windows)

base=https://github.com/kmcd/xray/releases/download/v${VERSION}
curl -LO ${base}/xray_${VERSION}_${OS}_${ARCH}.tar.gz
curl -LO ${base}/checksums.txt
curl -LO ${base}/checksums.txt.sig
curl -LO ${base}/checksums.txt.pem

cosign verify-blob \
  --certificate-identity-regexp 'https://github.com/kmcd/xray/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  checksums.txt

sha256sum -c --ignore-missing checksums.txt
tar -xzf xray_${VERSION}_${OS}_${ARCH}.tar.gz
sudo mv xray /usr/local/bin/
```

### Verifying provenance

Each release ships a single SLSA L3 build provenance attestation
(`xray.intoto.jsonl`) generated by the
[SLSA GitHub generator](https://github.com/slsa-framework/slsa-github-generator).
The file lists every release artifact as a subject (binaries, archives, SBOMs)
so one download verifies any platform you pick. Each archive also ships an
SPDX-JSON SBOM (`*.spdx.json`). Verify the attestation with
[`slsa-verifier`](https://github.com/slsa-framework/slsa-verifier):

```bash
curl -LO ${base}/xray.intoto.jsonl

slsa-verifier verify-artifact \
  --provenance-path xray.intoto.jsonl \
  --source-uri github.com/kmcd/xray \
  --source-tag "v${VERSION}" \
  "xray_${VERSION}_${OS}_${ARCH}.tar.gz"
```

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE).

`xray` is a read-only extraction tool. It never writes to any remote system and
never stores credentials or source content in the output artifact.

## FAQ

<!-- vale Google.FirstPerson = NO -->
<!-- vale Microsoft.FirstPerson = NO -->

**Can I run this against repositories with sensitive history?**\
Yes. `xray` reads git metadata — SHAs, timestamps, author handles, file paths, numstat. No diff text, no commit bodies, no file content is read or stored. See the full capture inventory in [`docs/security.md`](docs/security.md#2-no-source-content-stored).

**What if a provider returns 403 on a required endpoint?**\
The endpoint records `accessible: false` with the reason and emits no rows. The analyser treats absence as *unknown*, not *no signal* — a critical distinction for analyses that depend on data presence. The run continues. See [`docs/security.md`](docs/security.md#7-failure-modes-for-security-review).

**What happens if I revoke the token mid-run?**\
The next API call returns `401`. The connector records `accessible: false` on that endpoint and continues; subsequent calls are recorded identically. The run completes with exit code `2` (partial — artifact produced, errors in manifest). See [`docs/security.md`](docs/security.md#7-failure-modes-for-security-review).

**How do I verify the artifact has no secrets in it before sending?**\
Unpack the `.tar.gz`. `manifest.json` contains row counts, endpoint status, and provenance metadata — no credentials. A real example is at [`docs/sample-manifest.json`](docs/sample-manifest.json).

**What is in `manifest.json` vs. the SQLite database?**\
`manifest.json` is the run summary: schema version, extraction window, connector versions, per-endpoint access status, row counts, and errors. The SQLite DB is the metrics: commits, PRs, reviews, CI builds, error rates, observability signals. Full schema: [`docs/schema.md`](docs/schema.md).

**Does the tool need network access from inside my VPC?**\
Outbound HTTPS to configured providers (GitHub, Sentry, etc.) and to `github.com` for repo cloning. No inbound ports, no callbacks. Egress-only. See [`docs/spec.md`](docs/spec.md) for the full connector list.

**Can I keep the temp clones for inspection?**\
Pass `--keep-clones` to skip cleanup; clone paths are logged to stderr and recorded in the `.log` file. By default, clones are deleted after each repo finishes. See [`docs/spec.md`](docs/spec.md) → `xray run` → flags.

**Why a static binary instead of a script or container?**\
A single static binary has one thing to verify: one file, one SHA256, one cosign signature. No runtime dependencies to audit — no pip, no npm, no base image. See [`docs/threat-model.md`](docs/threat-model.md).

<!-- vale Microsoft.FirstPerson = YES -->
<!-- vale Google.FirstPerson = YES -->

## Build from source

See [`CONTRIBUTING.md`](./CONTRIBUTING.md#build-from-source).
