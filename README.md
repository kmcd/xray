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

# Full extraction. Produces ./xray-export-<UTC-timestamp>.tar.gz.
xray run xray.toml
```

The full configuration reference and behaviour spec live in [`CLAUDE.md`](./CLAUDE.md). The output schema is documented in [`docs/schema.md`](./docs/schema.md).

## Compatibility

The analyser refuses to load artifacts at an unknown `schema_version`. Pre-1.0, expect schema bumps as the model settles.

| xray version | schema_version |
| ------------ | -------------- |
| 0.1.0        | 1              |

## Build from source

```bash
make build       # produces ./xray
make test
make lint
```

Requires Go 1.23 or later. CGO is not used and is disabled in release builds.

## License

TBD.
