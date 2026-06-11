# Enterprise environments

This document covers how to run `xray` behind a corporate forward proxy,
under TLS interception, and in networks with egress restrictions. Audience:
the operator setting up `xray` and the security team controlling egress rules
on the machine running it.

## Forward proxy

`xray` uses Go's standard `net/http` transport for all connector API calls.
That transport reads proxy settings from environment variables automatically:

| Variable | Purpose |
|----------|---------|
| `HTTPS_PROXY` | Proxy for HTTPS traffic (preferred form) |
| `HTTP_PROXY` | Proxy for HTTP traffic |
| `NO_PROXY` | Comma-separated hostnames to bypass |

Set the variables in the shell before invoking `xray`:

```sh
export HTTPS_PROXY=http://proxy.corp.example.com:8080
export NO_PROXY=localhost,127.0.0.1
xray run --config xray.toml
```

`xray check` and `xray validate` also respect these variables.

### Git clone

Repository metadata requires cloning each repo. `xray` calls the `git`
binary for this step using an HTTPS address (`https://github.com/<owner>/<repo>.git`).
Git honors `HTTPS_PROXY` for HTTPS addresses, so the same variable set above
covers both the API calls and git clone.

`xray` clones over HTTPS only — SSH is not used. SSH proxy configuration
(`ProxyCommand`) is not required for SSH-based ambient credentials
(`ssh-agent`, `~/.ssh/config`).

The GitHub token in `[connectors.github]` authenticates API calls only;
clones rely on the operator's ambient git authentication. Configure
HTTPS credentials for `github.com` before running `xray check`:
`gh auth setup-git` (if `gh` is installed) or a credential helper
(`credential-osxkeychain`, `git-credential-manager`). Without one,
`xray check` reports a clone-access failure with an actionable hint.

## Custom CA bundle

Go's TLS stack loads trust anchors differently by platform.

### Linux

On Linux, Go reads the `SSL_CERT_FILE` environment variable. **Setting
`SSL_CERT_FILE` replaces the system CA pool entirely — it does not append to
it.** The file must contain both the standard root CAs and the corporate CA:

```sh
# Combine the system bundle with the corporate CA into a single file
cat /etc/ssl/certs/ca-certificates.crt /path/to/corp-ca.pem \
    > /tmp/xray-ca-bundle.pem

export SSL_CERT_FILE=/tmp/xray-ca-bundle.pem
xray run --config xray.toml
```

`SSL_CERT_DIR` accepts a colon-separated list of directories of PEM files;
the same replacement semantics apply.

### macOS

On macOS, Go reads the system keychain via the Security framework — `SSL_CERT_FILE`
is not honored. Add the corporate root CA to the system keychain:

```sh
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain /path/to/corp-ca.pem
```

The change takes effect immediately for new processes.

### Git clone TLS

`git` uses its own CA bundle, independent of Go's. Configure it separately:

```sh
# Linux: point git at the same combined bundle
export GIT_SSL_CAINFO=/tmp/xray-ca-bundle.pem
xray run --config xray.toml
```

Or write the path into the global git config once:

```sh
git config --global http.sslCAInfo /tmp/xray-ca-bundle.pem
```

On macOS, git also reads the system keychain (via the macOS Security
framework) — adding the CA via `security add-trusted-cert` covers both Go
and git.

## Firewall allowlist

The table below lists every external host `xray` contacts. Only connectors
present in `xray.toml` are contacted; unused connectors make no outbound
calls. All connections use TLS on port 443.

| Connector block | Hosts |
|-----------------|-------|
| `[connectors.github]`, `[connectors.github_actions]` | `api.github.com` |
| git clone (always, for any configured repo) | `github.com` |
| `[connectors.circleci]` | `circleci.com` |
| `[connectors.bugsnag]` | `api.bugsnag.com` |
| `[connectors.honeycomb]` | `api.honeycomb.io` |
| `[connectors.sentry]` | `sentry.io` |

Git clone uses `github.com` over port 443 (HTTPS). Port 22 (SSH) is not
used.

**Self-hosted Sentry and on-prem Bugsnag.** xray v1 contacts `sentry.io`
and `api.bugsnag.com` only. Per-instance endpoint overrides are not
configurable in v1.

**Honeycomb EU region.** The EU endpoint (`api.eu1.honeycomb.io`) is not
configurable in v1. US region only.

## See also

- [docs/security.md § Network surface](./security.md#6-network-surface) —
  the specific REST and GraphQL endpoints within each host.
- [docs/spec.md](./spec.md) — full configuration reference.
- [docs/threat-model.md](./threat-model.md) — trust boundaries and
  network access analysis.
