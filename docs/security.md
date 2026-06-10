# Security and privacy

This document describes what `xray` captures, what it cannot capture, and the
guarantees the binary makes to a security reviewer. The audience is the
customer's security or platform team: someone who needs to approve `xray`
before it runs against the customer's repos, CI logs, and error tracker.

For vulnerability reporting, see [SECURITY.md](../SECURITY.md). For the
threat model, see [docs/threat-model.md](./threat-model.md). For example
artifacts a real run produces, see [docs/sample-manifest.json](./sample-manifest.json)
and [docs/sample-run.log](./sample-run.log). For the consultant-side
counterpart — what happens to the artifact after the customer sends it —
see [docs/engagement-guide.md](./engagement-guide.md).

## 1. Read-only

`xray` issues only read calls against every configured connector. This is
enforced by code review against a fixed list of forbidden method-name
prefixes on the provider SDKs.

For the GitHub connector, every `go-github` method whose name starts with
`Create`, `Update`, `Delete`, `Edit`, `Add`, or `Remove` is forbidden. The
codebase does not import or call any such method. The same rule covers
Sentry, Bugsnag, Honeycomb, CircleCI, and GitHub Actions: only read paths
are wired.

If a connector token is granted write scope (e.g. a `repo`-scope GitHub
personal access token), `xray` still issues only read calls. Write scope is
not used. The `xray check` command surfaces the granted scope so the
operator can downgrade to a read-only token when the provider supports one
(e.g. GitHub fine-grained tokens with `Read` permissions).

No `POST`, `PATCH`, `PUT`, or `DELETE` is issued against any provider at
any point in the run.

## 2. No source content stored

The artifact does not contain source code, diff text, patch text, PR or
commit message bodies, review comments, or issue body text.

What is captured instead, per data type:

- **Commits.** Per-commit metadata (SHA, authored/committed timestamps,
  numstat totals, signature-verified flag) and the parsed message
  *subject* line (the first newline-terminated segment, typically under
  72 characters). The full message body is parsed at extract time to
  derive structured signals — `is_revert`, `reverts_sha`,
  `has_hotfix_marker`, co-author trailers — then the body variable
  drops out of scope. It is never persisted.
- **Commit files.** Per-file numstat only: path, additions, deletions,
  change type, previous path on rename. No patch text. The numstat is
  what `git log --numstat --name-status` returns; rerunning `xray`
  against the same range emits identical rows.
- **Pull requests.** Title, structural counts derived from the body
  (length, code-block count, image count, link count, issue-reference
  count, checklist totals, risk-marker flag, template-match score). The
  body text itself is parsed in-process and discarded before insertion.
- **Reviews and PR comments.** Timestamps, author handle, structural
  counts, path and `in_reply_to` for review-thread comments. Comment
  body text is never persisted.
- **Incidents (Sentry, Bugsnag).** Issue metadata, timestamps, severity,
  first-seen / last-seen, optional `culprit_ref`. Error messages and
  stack-trace bodies are not captured.
- **Builds and deploys.** Workflow run identifiers, status, durations,
  conclusions. Console logs are not retrieved.
- **File metrics.** A working-tree walk at `head_sha` emits per-file
  size, language classification (via go-enry, a pure-Go Linguist port),
  binary flag, and dependency-manifest flag (allow-list filename match).
  File *contents* are not stored.

The single documented exception is the `harness_artifacts.content` column:
the literal text of CI configuration files (`.github/workflows/*.yml`,
`.circleci/config.yml`, `Dockerfile`, etc.). This is opt-in via the config
flag `capture_harness_content = true` and **off by default**. When the
flag is off the column is empty and the file text is never read.

Patches and diffs stay in the customer's git clones. The per-run clone
directory (`/tmp/xray-<run_id>/`) is removed at the end of the run unless
the operator passes `--keep-clones` for debugging.

## 3. No secrets in the artifact

Connector tokens are read from the operator's TOML config file. The config
file is generated, edited, and run by the customer in their own
environment. It is never written to the artifact, never logged, and never
transmitted to anywhere other than the target API endpoints.

The artifact (`xray-export-<UTC-timestamp>.tar.gz`) contains exactly two
files:

- `manifest.json` — run metadata, repository slugs, row counts, and the
  per-connector `extraction_provenance` block. No tokens. No request
  headers. No environment-variable contents.
- `metrics.sqlite` — the canonical data tables described in
  [docs/schema.md](./schema.md). No tokens. No request headers.

The same guarantee covers the sibling `.log` file (mirroring stderr at
info level). Tokens are filtered at every log site; logs record method
names, endpoint paths, status codes, and durations, never authorization
headers or query-string credentials. Verify with:

```bash
grep -iE 'token|secret|bearer|authorization' xray-export-*.log
# zero matches
```

If a customer chooses to send the artifact back for analysis, the
contents are bounded by what is in those two files. The operator can
inspect both before sending.

## 4. Per-individual data

All measurement is team and system level. The artifact contains no
per-individual ranking, no per-individual aggregation table, and no
named-developer rollups.

Where developer identity is required for linkage (e.g. associating
commits to PRs, attributing reviews to the reviewer who left them),
`xray` stores opaque `*_handle` strings. These are SHA-256 hashes of the
original git ident or login, truncated to 15 decimal digits, of the form
`h_<15 digits>`. The hashing is one-way and the raw login is discarded
in the same function. Downstream analysers treat `*_handle` as a join key
only; team-level rollups happen against the `teams` table.

The schema explicitly omits per-individual aggregation tables. The
analyser contract (`schema_version`, documented in
[docs/schema.md](./schema.md)) refuses any artifact attempting to
introduce them.

A `.mailmap` file checked into the repo, where present, is honoured so
co-author aliases collapse correctly. The `manifest.mailmap_applied`
field records whether the mailmap was applied across every repo in the
run; downstream analyses use the flag to caveat author-universe
findings when it was not.

## 5. Logs

`xray run` writes a sibling `.log` file alongside the artifact, mirroring
stderr at `info` level. The log captures per-phase boundaries, per-repo
progress, rate-limit waits, and per-connector summaries. Suppress with
`--no-run-log`; quiet the stream entirely with `--output quiet`.

The log does not contain tokens, secrets, bearer headers, OAuth refresh
tokens, or environment-variable contents at any level. The guarantee is
enforced by code review against the small set of `slog` call sites in
`internal/run`, `internal/connectors/*`, and `internal/cli`. Adding a new
log site that emits a token would be caught at review.

A representative log is checked into the repo at
[docs/sample-run.log](./sample-run.log). It demonstrates the format and
can be `grep`-verified for the absence of credential material.

## 6. Network surface

`xray` connects only to provider APIs the customer configures. Each
connector reaches a fixed set of endpoints; the per-run
`extraction_provenance.endpoints` map records which were accessible.

| Connector | Hosts | Endpoints |
|-----------|-------|-----------|
| github | `api.github.com`, `github.com` (git clone) | GraphQL `prListQuery`; REST `/repos/{owner}/{repo}` (metadata, branches, branch protection, releases, contributors, languages, PR template, CODEOWNERS) |
| github_actions | `api.github.com` | REST `/repos/{owner}/{repo}/actions/runs`, `/jobs`, `/deployments`, `/deployments/{id}/statuses` |
| circleci | `circleci.com` | REST `/api/v2/project/{slug}/pipeline` |
| sentry | `sentry.io` or self-hosted | REST `/api/0/projects/{org}/{proj}/issues/` |
| bugsnag | `api.bugsnag.com` | REST `/projects/{id}/errors` |
| honeycomb | `api.honeycomb.io` or EU host | REST `/1/markers/{dataset}` |

Git clones reach `github.com` (or the customer's GHE host) over the git
protocol, using the operator's ambient git authentication — SSH keys,
credential helper, `gh` CLI. The GitHub API token in the config is used
for API access only, not for clone.

Every connector wraps its HTTP transport with the rate-limit and
retry policy described in [docs/spec.md](./spec.md#behaviour): `429` and
`5xx` retry with exponential backoff and jitter, capped at three
attempts per request and 60 seconds cumulative wait per request; any
other `4xx` is permanent failure for that request, recorded in
provenance, and the run continues.

For runtime verification of which endpoints a specific run actually
called, inspect `manifest.json` →
`extraction_provenance[].endpoints` — each entry is keyed by the
internal endpoint name and carries `accessible` and an optional `reason`
when not.

## 7. Failure modes for security review

The behaviours below are what a reviewer should expect under common
failure conditions. They are deliberate; none of them silently extract,
log, or transmit data outside the documented surface.

- **Token revoked mid-run.** The next API call returns `401`. The
  connector records `EndpointStatus{Accessible: false, Reason: "..."}`
  on the affected endpoint and continues; subsequent calls return `401`
  identically and are recorded the same way. No retry storm. The run
  completes with `exit 2` (partial — artifact produced, connector
  errors recorded). No token-rotation logic, no automatic re-auth.
- **Token granted write scope.** `xray check` reports the granted scope
  in the operator-visible output. The connectors still issue only read
  calls. No write call is ever attempted, regardless of granted scope.
- **Permission-gated endpoint inaccessible (403/404).** The endpoint
  records `EndpointStatus{Accessible: false, Reason: "..."}` and emits
  no rows. The analyser treats the absence as **unknown**, not as
  **no signal** — a critical distinction for analyses that depend on
  the data being there to mean something.
- **Network drops mid-walk.** The in-flight request fails after retry
  exhaustion. The connector marks
  `PaginationComplete = false` for the affected endpoint and continues
  with whatever rows it had already collected. Re-running the extraction
  is the recovery path; there is no incremental state to corrupt.
- **Rate limit hit.** The transport blocks until the
  `X-RateLimit-Reset` window expires, logged at `info` level so the
  operator can see why the run is slow. If the cumulative wait would
  exceed the per-request budget, `RateLimitTruncated = true` is set on
  the provenance and the request fails permanently for that endpoint.
- **Connector misconfigured.** `xray validate` (offline) and
  `xray check` (live) surface the failure before `xray run` ever
  attempts extraction. `validate` is fully offline and reads no
  network.
- **Provider returns malformed data.** Per-row insertion errors are
  recorded in `Provenance.Errors[<context>]` and the walk continues to
  the next row. A single bad row does not abort the connector. Row
  counts in `manifest.counts` reflect only successful inserts.
- **Forced termination.** `Ctrl-C` triggers a cooperative cancel:
  in-flight goroutines drain to the nearest checkpoint, the per-run
  temp directory is removed (unless `--keep-clones`), and the run
  exits `130`. A second signal during drain skips cleanup and exits
  immediately; the operator can remove the temp directory manually.
- **Disk full.** The SQLite write fails. The run exits `3` (fatal). No
  partial artifact is shipped — the temp directory is removed unless
  `--keep-clones`.

The artifact at the end of any of the above carries an
`extraction_provenance` block that records exactly what happened. No path
silently writes partial data without recording it.

## Auditing the binary

The release archives ship with `checksums.txt` signed by cosign (keyless,
via Sigstore's transparency log). The recommended verification flow is
in the README's [Verifying the binary](../README.md#verifying-the-binary) section.

`xray version` embeds the build commit and date via Go's standard
`-ldflags` injection. The binary is built with `CGO_ENABLED=0`; no
native extensions, no shared libraries, no dynamic loader hooks.

The source tree under audit is the GitHub repository at the tagged
release. Code review concentrates on:

- `internal/connectors/*` — every API method called
- `internal/run` — orchestration, the temp-directory lifecycle, signal
  handling
- `internal/manifest` — the artifact shape
- `internal/cli` — operator-facing output and logging

The full configuration reference and behaviour spec live in
[docs/spec.md](./spec.md); the schema reference lives in
[docs/schema.md](./schema.md).
