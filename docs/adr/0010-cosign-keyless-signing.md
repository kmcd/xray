## Status

Accepted. v0.1.0.

## Context

Release artifacts needed to be signed to support checksum verification
without managing a long-lived signing key.

## Decision

Release pipeline uses `cosign sign-blob` in OIDC keyless mode against the
goreleaser-produced `checksums.txt`. No long-lived signing key to manage;
GitHub Actions' OIDC issuer is the trust anchor.

Verification uses:
```
cosign verify-blob --certificate-identity-regexp 'https://github.com/kmcd/xray/.*'
```

Only `checksums.txt` is signed, not individual archives. Verification is
"verify checksums.txt is signed by us, then sha256 the archive".

## Consequences

**Positive.** No long-lived signing key to manage. GitHub Actions OIDC is
the trust anchor.

**Negative.** Requires `id-token: write` permission in the release workflow.
Only `checksums.txt` is signed, not individual archives.

**Neutral.** Verification uses `cosign verify-blob`.

## How to apply

`.github/workflows/release.yml` — `cosign sign-blob` step with keyless mode.
`README.md` — verification instructions.
