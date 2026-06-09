# Security Policy

This document is the vulnerability-reporting policy. For the full description of
what `xray` captures, what it does not capture, and the threat model a customer's
security team should review before running the binary, see
[`docs/security.md`](docs/security.md) and
[`docs/threat-model.md`](docs/threat-model.md).

## Supported Versions

Pre-1.0, only the latest minor release receives security fixes.

| Version | Supported |
| ------- | --------- |
| latest minor | yes |
| older minors | no |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report security issues by email to **security@dancingtext.com** or via
[GitHub Security Advisories](https://github.com/kmcd/xray/security/advisories/new).

Expected response times:

- **Acknowledgement**: within 72 hours
- **Fix for high/critical severity**: within 30 days of confirmed report
- **Fix for medium/low severity**: best effort; included in next scheduled release

We will coordinate disclosure timing with the reporter.

## Scope

In scope:

- The `xray` binary itself (data extraction, credential handling, output artifact generation)
- The extraction process and any credentials or tokens read from the config file
- The read-only invariant: the tool must never write to any remote system

Out of scope:

- Downstream use of the `.tar.gz` artifact (analysis, reporting, consultant workflows)
- The client's own infrastructure that `xray` connects to
- Third-party connectors' own APIs (report those to the respective vendors)

## Token Handling

`xray` reads API tokens from a local config file that never leaves the client machine.
Tokens are used only for authenticated read calls and are never logged, stored in the
output artifact, or transmitted anywhere other than the target API endpoint.
