# xray

Read-only extractor that produces a portable metrics artifact from a client's engineering systems (git history, GitHub PRs and reviews, CI builds, error tracker, observability).

The artifact is a single `.tar.gz` containing a SQLite database and a JSON manifest. It contains no source code and no secrets. Nothing leaves the client's environment except the artifact, and only when the client chooses to send it.

## Status

Pre-1.0. The schema is unstable; minor version bumps may introduce breaking schema changes. See the compatibility table below.

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
- [`docs/sample-manifest.json`](docs/sample-manifest.json) — a real,
  redacted `manifest.json` showing the `extraction_provenance` block
  with mixed-state endpoints, real row counts, and an error entry.
- [`docs/sample-run.log`](docs/sample-run.log) — the matching `.log`
  file demonstrating no tokens, per-phase logs, a rate-limit wait, a
  permission-gated 403 → `EndpointStatus{accessible: false}` flow,
  and the post-run summary.

## License

TBD.
