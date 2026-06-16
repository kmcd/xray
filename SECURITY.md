# Security policy

This document is the vulnerability-reporting policy. For the full description of
what `xray` captures, what it does not capture, and the threat model a customer's
security team should review before running the binary, see
[`docs/security.md`](docs/security.md) and
[`docs/threat-model.md`](docs/threat-model.md).

## Supported versions

Pre-1.0, only the latest minor release receives security fixes.

| Version | Supported |
| ------- | --------- |
| latest minor | yes |
| older minors | no |

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report security issues by email to **security@dancingtext.com** or via
[GitHub Security Advisories](https://github.com/kmcd/xray/security/advisories/new).

Expected response times:

- **Acknowledgement**: within 72 hours
- **Fix for high/critical severity**: within 30 days of confirmed report
- **Fix for medium/low severity**: best effort; included in next scheduled release

We will coordinate disclosure timing with the reporter.

## Scope of coverage

In scope:

- The `xray` binary itself (data extraction, credential handling, output artifact generation)
- The extraction process and any credentials or tokens read from the config file
- The read-only invariant: the tool must never write to any remote system

Out of scope:

- Downstream use of the `.tar.gz` artifact (analysis, reporting, consultant workflows)
- The client's own infrastructure that `xray` connects to
- Third-party connectors' own APIs (report those to the respective vendors)

## Token handling

`xray` reads API tokens from a local config file that never leaves the client machine.
Tokens are used only for authenticated read calls and are never logged, stored in the
output artifact, or transmitted anywhere other than the target API endpoint.

## Network behaviour

`xray` issues only read calls against every configured connector. The binary contains no
`POST`, `PATCH`, `PUT`, or `DELETE` code paths. This is not a configuration option or a
policy decision that depends on the granted token scope — the write paths do not exist.

For the GitHub connector, every `go-github` method whose name starts with `Create`,
`Update`, `Delete`, `Edit`, `Add`, or `Remove` is forbidden by code review. The same
rule applies to Sentry, Bugsnag, Honeycomb, CircleCI, and GitHub Actions: only read
paths are wired.

If a token is granted write scope (for example, a `repo`-scope GitHub personal access
token), `xray` still issues only read calls. The `xray check` command reports the
granted scope so the operator can downgrade to a read-only token when the provider
supports one.

## Body discipline

PR bodies, commit message bodies, and review comment bodies are parsed at extract time
to derive structured signals. The raw text is never written to SQLite or to
`manifest.json`.

What is captured instead:

- **Commit message bodies.** Parsed to derive `is_revert`, `reverts_sha`,
  `has_hotfix_marker`, and co-author trailer counts. The body variable drops out of
  scope in the same function and is never persisted.
- **PR bodies.** Parsed to derive structural counts: body length, code-block count,
  image count, link count, issue-reference count, checklist totals, risk-marker flag,
  and template-match score. The body text is discarded before insertion.
- **Review and PR comment bodies.** Parsed to derive length and structured counts.
  Comment text is never persisted.

This discipline holds regardless of what the bodies contain. A PR body that includes
source code, credentials, or internal data does not cause that content to appear in the
artifact.

## Artifact contents

The output file (`xray-export-<UTC-timestamp>.tar.gz`) contains exactly two files:

- `manifest.json` — run metadata, repository slugs, row counts, and the per-connector
  `extraction_provenance` block. No tokens. No request headers. No raw bodies.
- `metrics.sqlite` — the extracted data tables described in
  [`docs/schema.md`](docs/schema.md). No tokens. No source code. No raw PR or commit
  bodies.

The artifact does not contain source code, diff text, patch text, API tokens,
authorization headers, or raw PR, commit, or review comment bodies.

The operator can inspect both files before sending the artifact to anyone:

```bash
tar -tzf xray-export-*.tar.gz   # list contents — should show exactly two files
sqlite3 metrics.sqlite .tables  # inspect the schema
```

## Verifying the binary

Each release ships `checksums.txt` signed by cosign in keyless mode (Sigstore
transparency log, GitHub Actions OIDC issuer).

To verify before running:

```bash
VERSION=0.4.8
OS=linux        # or darwin
ARCH=amd64      # or arm64

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
```

The `--certificate-identity-regexp` flag pins the signing certificate's identity to the
`github.com/kmcd/xray` repository. A binary signed by a different identity fails
verification.

`scripts/install.sh` runs the same verification automatically when
`XRAY_VERIFY_COSIGN=1` is set.

Each release also ships a SLSA L3 build provenance attestation (`xray.intoto.jsonl`).
See [README → Verifying the binary](README.md#verifying-the-binary) for the full
verification flow including SLSA provenance.
