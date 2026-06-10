# Threat model

A single-page model of the trust boundaries around `xray` and the
attack surface a security reviewer should care about. Tone is
descriptive: what an attacker at each position could do, what they
could not, and what changes if a given component is compromised.

For the catalogue of guarantees and failure modes, see
[docs/security.md](./security.md). For the manifest a real run
produces, see [docs/sample-manifest.json](./sample-manifest.json).

## Trust boundaries

```
+------------------+      +-----------------+      +------------------+      +-------------------+
|  customer machine|  →   |  provider APIs  |  →   |   artifact       |  →   | consultant        |
|  (xray runs here)|      | (github, etc.)  |      | (.tar.gz)        |      | analyser          |
+------------------+      +-----------------+      +------------------+      +-------------------+
        ↑                       ↑                        ↑                         ↑
        |                       |                        |                         |
        config (TOML,           authenticated            artifact integrity        schema_version
        tokens) — never         read calls only          via sha256 of the         contract refused
        leaves machine                                   .tar.gz                   on mismatch
```

Five trust boundaries. Two are crossed by `xray` itself; two are crossed
only by the customer's explicit choice; the fifth is the binary itself.

1. **Customer machine ↔ provider APIs.** Crossed by `xray` on every
   API call. Authentication is by token (config-resident). Only `GET`
   methods are issued; see [security.md §1](./security.md#1-read-only).
2. **Provider APIs ↔ artifact.** Crossed by `xray` at insertion time.
   Parsed structured data passes through; raw bodies, patches, and
   tokens do not. See [security.md §2](./security.md#2-no-source-content-stored)
   and [§3](./security.md#3-no-secrets-in-the-artifact).
3. **Customer machine ↔ artifact on disk.** The artifact is written
   under the customer's working directory. The customer chooses when,
   how, and whether to share it.
4. **Customer ↔ consultant.** Crossed only when the customer
   explicitly transmits the artifact. `xray` neither uploads nor
   notifies any third party.
5. **Source ↔ binary.** The release archive is built in GitHub
   Actions from a tagged commit; `checksums.txt` is signed by cosign
   (keyless, Sigstore). The customer's reviewer verifies the
   signature, then verifies the binary against the checksum, then
   runs it. See [README → Verifying the binary](../README.md#verifying-the-binary).

## Attack surface

| Attacker position | What they can do | What they cannot do |
|---|---|---|
| Controls the `xray` binary | Anything the operator's tokens allow under the granted scope, on the configured providers | Exceed the granted token scope (no privilege escalation); write to providers (no `POST`/`PATCH`/`PUT`/`DELETE` code paths exist); reach providers not in the config (no exfil to unknown hosts) |
| Controls a provider's HTTP response | Inject malformed JSON, oversized pages, slow-loris responses; cause `xray` to record per-row errors and skip rows | Inject content into the SQLite database past the schema's type constraints; cause `xray` to write to a provider; cause `xray` to log a token |
| Controls a provider account's data | Populate any captured field with attacker-chosen values (e.g. a commit message subject containing arbitrary text) | Cause `xray` to fetch outside the configured scope; persist patch / diff / body text in the artifact |
| Reads the artifact in transit | Read everything in the SQLite database and manifest | Read tokens (not present); read raw bodies / patches / diffs (not present); read per-individual rankings (do not exist) |
| Reads the artifact at rest on the customer machine | Same as in-transit | Same as in-transit |
| Compromises the customer's machine | Read the config file and its tokens directly; replace the `xray` binary; intercept the artifact before it's sent | None of these are made worse by `xray`'s presence — the attacker already controls the machine |

## What changes if the binary is malicious

A malicious build's blast radius is bounded by the granted token scope
on the configured providers. A token with `repo` scope on `kmcd/foo`
gives the binary the same access the operator could exercise via the
`gh` CLI from the same machine. It does not give the binary access to:

- providers not listed in the config (no implicit network)
- repositories the token does not cover (the GitHub API enforces this)
- write methods (per provider's API: the methods require write scope;
  per the customer's review: the token can be scoped read-only)
- the customer's other secrets on disk (the binary reads the
  config file passed on the command line and, within each cloned
  repo, declared configuration and tooling manifests — workflow
  YAML, dependency manifests, harness config files; never
  application logic)

This is why we recommend a fine-grained, read-only token. With a
read-only token, a compromised binary cannot mutate anything on the
provider — the worst-case is data exfiltration to a third party,
detectable as outbound traffic to a host not in the table above.

Mitigation: verify the binary against the cosign-signed checksum
before running. The signing certificate's identity is pinned to
`https://github.com/kmcd/xray/.*` issued by GitHub Actions'
OIDC issuer, so a malicious binary signed by a different identity
fails verification.

## What changes if the artifact is leaked

The artifact is a SQLite database plus a JSON manifest. A leak
exposes:

- Repository slugs the customer configured
- Per-repository commit history within the configured window
  (SHAs, timestamps, numstat totals, the commit message *subject*
  line)
- Per-repository PR / review / build / deploy / incident counts and
  timing
- Opaque `*_handle` hashes (one-way SHA-256 of the original login,
  truncated)
- Team-to-repository mapping the operator configured

A leak does not expose:

- Source code, diffs, or patches (not captured)
- Commit message bodies, PR bodies, review comment bodies (not
  captured)
- Tokens or credentials (not captured)
- Per-individual rankings (not produced)
- Original logins or git idents (one-way hashed at the boundary)

A leak is therefore comparable in sensitivity to the customer's
public commit metadata for an open-source repo, plus team
membership and aggregate productivity counts for a closed-source
one. Customers treating commit timestamps as sensitive (e.g.
release-cadence intelligence for a regulated product) should treat
the artifact as confidential.

## Supply chain

The release pipeline runs in GitHub Actions on
`github.com/kmcd/xray`. Each tagged release produces:

- Per-platform binaries (linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64, optionally windows/amd64)
- `checksums.txt` with SHA-256 per asset
- `checksums.txt.sig` + `checksums.txt.pem` from cosign (keyless,
  Sigstore transparency log)
- An SLSA L3 build-provenance attestation (post-supply-chain issue
  lands)
- A Software Bill of Materials (SBOM) listing every Go module in
  the binary

Build inputs are pinned: `go.mod` and `go.sum` are checked in;
GitHub Actions workflow versions are pinned by SHA; the release
runner uses a published GitHub-Actions hosted-runner image. The
binary is built with `CGO_ENABLED=0`; no native extensions are
linked. The dependency graph is intentionally small — the full set
is documented in [docs/adr/0003-library-set.md](./adr/) (locked
at v0.1.0).

Dependabot is enabled on the repository; security advisories on
any pinned dependency trigger a PR within hours of publication.
CodeQL runs on every push to `main`.

## Out of scope

This threat model covers `xray` only. The following are intentionally
out of scope:

- **Downstream analyser.** The consultant's analysis stack runs
  against the artifact; its security posture is the consultant's
  responsibility and is documented separately by the consulting
  engagement.
- **Consultant workflow.** How the artifact is transmitted, stored,
  and eventually destroyed depends on the engagement contract. The
  customer should treat the artifact as confidential by default and
  agree the retention policy with the consultant up front. The
  consultant-side playbook lives in
  [docs/engagement-guide.md](./engagement-guide.md).
- **Customer's own infrastructure.** Provider security posture,
  network egress controls, secrets management, and the customer's
  endpoint security are the customer's responsibility. `xray`
  inherits whatever the customer's machine offers.
- **Third-party provider APIs.** GitHub, Sentry, Bugsnag, Honeycomb,
  and CircleCI each have their own security posture, vulnerability
  reporting, and threat model. Vulnerabilities in those providers
  are out of scope for `xray`; report them to the respective
  vendors.

## See also

- [docs/security.md](./security.md) — the per-guarantee detail
- [docs/sample-manifest.json](./sample-manifest.json) — what a real
  artifact looks like
- [docs/sample-run.log](./sample-run.log) — what a real run logs
- [docs/engagement-guide.md](./engagement-guide.md) — the
  consultant-side playbook for using an artifact
- [SECURITY.md](../SECURITY.md) — vulnerability reporting policy
