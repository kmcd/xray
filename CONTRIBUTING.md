# Contributing

Thank you for your interest in `xray`.

## What's welcome

- **Bug reports** — use the bug report issue template; include version, command, and a sanitized config snippet
- **Security reports** — see [SECURITY.md](SECURITY.md); do not file public issues for vulnerabilities
- **Documentation fixes** — typos, unclear phrasing, broken links

## What's not (pre-1.0)

`xray` is a consulting tool with a deliberately narrow scope. The product spec in
[`docs/spec.md`](docs/spec.md) is intentionally constrained. We're not accepting:

- **Feature additions** — roadmap is driven by engagement requirements, not community requests
- **New connectors** — connector additions are out of scope for community PRs in v0.x
- **Scope expansions** — anything that adds ML/NLP, individual-developer metrics, write operations,
  or source-content storage conflicts with the settled design

If you're unsure whether something fits, open a discussion before writing code.

## Stability expectations

Before 1.0, both the CLI surface and the output schema change without notice. The
`schema_version` integer in `manifest.json` is the analyser contract; the binary
version tracks the binary only. Expect bumps.

## Code of Conduct

This project follows the [Contributor Covenant v2.1](CODE_OF_CONDUCT.md).
