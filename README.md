# xray

Read-only extractor that produces a portable metrics artifact from a client's engineering systems (git history, GitHub PRs and reviews, CI builds, error tracker, observability).

The artifact is a single `.tar.gz` containing a SQLite database and a JSON manifest. It contains no source code and no secrets. Nothing leaves the client's environment except the artifact, and only when the client chooses to send it.

## Guarantees

- **Read-only**: no writes to any provider, ever.
- **No source content**: structural metadata and parsed fields only.
- **Team-level only**: no per-individual rollups.
- **Verifiable**: SHA256 + cosign on the binary; per-row provenance in the manifest.

## Status

Pre-1.0. The schema is unstable; minor version bumps may introduce breaking schema changes. See the compatibility table below.

## What xray does not do

- Issue any `POST`, `PATCH`, `PUT`, or `DELETE` to any provider — read calls only, even when the token has write scope.
- Capture source code, diff text, PR bodies, commit message bodies, or any text body marked sensitive. Parsed at extract time; dropped before the variable leaves scope.
- Store or transmit secrets. The token in `xray.toml` never leaves the machine. The artifact is a `.tar.gz` of a SQLite DB and a JSON manifest — no tokens, no environment variables, no source.
- Aggregate per-individual data. The artifact carries team-level rollups; opaque `*_handle` strings exist only for linkage, never for ranking.
- Run as a daemon, scheduled job, or web service. CLI only. Idempotent. Each run is full within the window.

## Install

Download the release for your platform, verify the cosign signature on `checksums.txt`, then verify the archive against the checksum.

```bash
VERSION=0.1.0
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

## Usage

```bash
# Generate a starter config from a GitHub org.
xray init --org my-org --out xray.toml --token "$GITHUB_TOKEN"

# Hand-edit xray.toml: connector tokens, project mappings, team layout.

# Syntactic + schema check, offline.
xray validate xray.toml

# Live preflight against configured connectors.
xray check xray.toml

# Full extraction. Produces ./xray-export-<UTC-timestamp>.tar.gz and a
# sibling .log file mirroring stderr (suppress with --no-run-log).
# On a TTY, the default (--output auto) renders a live (repo × connector)
# status grid:
#
#   xray run · elapsed 04:12 · ETA 14:47 ±2m · 3/4 workers
#
#   repo                clone           github           gh_actions       sentry
#   kmcd/foo            ✔ done          ✔ 4213 rows      ● gh_actions     ✔ 312 rows
#   kmcd/bar            ✔ done          ● prs            ▢ pending        🔒 inaccessible
#   kmcd/baz            ● clone         ▢ pending        ▢ pending        ▢ pending
#
# Non-TTY (CI, pipe to file) falls back to a stderr log with one line
# per phase boundary; force the log explicitly with --output log.
xray run xray.toml

# Machine-readable output. One NDJSON event per progress tick, terminated
# by a {"kind":"run_summary",...} object on stdout. See docs/spec.md.
xray run xray.toml --output json | jq .

# Quiet success: only the artifact path is written to stdout.
xray run xray.toml --output quiet
```

Exit codes: `0` clean, `1` config / pre-flight error, `2` partial run
(artifact produced, connector error recorded in manifest), `3` fatal.
See [`docs/spec.md`](./docs/spec.md) → `xray run` → "Exit codes".

The full configuration reference and behaviour spec live in [`docs/spec.md`](./docs/spec.md). The output schema is documented in [`docs/schema.md`](./docs/schema.md). Agent-facing constraints (invariants, non-goals, schema-versioning rules) live in [`CLAUDE.md`](./CLAUDE.md).

## Compatibility

The analyser refuses to load artifacts at an unknown `schema_version`. Pre-1.0, expect schema bumps as the model settles. Per-release changes that affect downstream consumers are tracked in [`CHANGELOG.md`](./CHANGELOG.md).

| xray version | schema_version |
| ------------ | -------------- |
| 0.1.0        | 1              |
| 0.2.0        | 1              |
| 0.2.1        | 1              |
| 0.2.2        | 1              |
| 0.3.0        | 2              |

`0.3.0` is the first release at `schema_version = 2`. Analysers pinned to `schema_version = 1` will refuse to load `0.3.0+` artifacts — see the [CHANGELOG](./CHANGELOG.md#030--2026-06-08) for the author-handle semantics shift driving the bump.

## Build from source

```bash
make build       # produces ./xray
make test
make lint
```

Requires Go 1.23 or later. CGO is not used and is disabled in release builds.

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

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

## License

Apache-2.0. See [LICENSE](LICENSE).

`xray` is a read-only extraction tool. It never writes to any remote system and
never stores credentials or source content in the output artifact.

## FAQ

**Why a static binary instead of a script or container?**\
A single static binary has one thing to verify: one file, one SHA256, one cosign signature. No runtime dependencies to audit — no pip, no npm, no base image. See [`docs/threat-model.md`](docs/threat-model.md).

**What happens if I revoke the token mid-run?**\
The next API call returns `401`. The connector records `accessible: false` on that endpoint and continues; subsequent calls are recorded identically. The run completes with exit code `2` (partial — artifact produced, errors in manifest). See [`docs/security.md`](docs/security.md#7-failure-modes-for-security-review).

**Can I run this against repositories with sensitive history?**\
Yes. `xray` reads git metadata — SHAs, timestamps, author handles, file paths, numstat. No diff text, no commit bodies, no file content is read or stored. See the full capture inventory in [`docs/security.md`](docs/security.md#2-no-source-content-stored).

**Does the tool need network access from inside my VPC?**\
Outbound HTTPS to configured providers (GitHub, Sentry, etc.) and to `github.com` for repo cloning. No inbound ports, no callbacks. Egress-only. See [`docs/spec.md`](docs/spec.md) for the full connector list.

**What if a provider returns 403 on a required endpoint?**\
The endpoint records `accessible: false` with the reason and emits no rows. The analyser treats absence as *unknown*, not *no signal* — a critical distinction for analyses that depend on data presence. The run continues. See [`docs/security.md`](docs/security.md#7-failure-modes-for-security-review).

**Can I keep the temp clones for inspection?**\
Pass `--keep-clones` to skip cleanup; clone paths are logged to stderr and recorded in the `.log` file. By default, clones are deleted after each repo finishes. See [`docs/spec.md`](docs/spec.md) → `xray run` → flags.

**How do I verify the artifact has no secrets in it before sending?**\
Unpack the `.tar.gz`. `manifest.json` contains row counts, endpoint status, and provenance metadata — no credentials. A real example is at [`docs/sample-manifest.json`](docs/sample-manifest.json).

**What is in `manifest.json` vs. the SQLite database?**\
`manifest.json` is the run summary: schema version, extraction window, connector versions, per-endpoint access status, row counts, and errors. The SQLite DB is the metrics: commits, PRs, reviews, CI builds, error rates, observability signals. Full schema: [`docs/schema.md`](docs/schema.md).
