## Status

Accepted. v0.2.x. First shipped for `internal/connectors/githubactions` (#66).

Note: this decision was originally numbered 023 in `tmp/adr.md` (a duplicate
of ADR 023 — author handles hashed). Renumbered to 027 as part of the
`docs/adr/` extraction in issue #102.

## Context

The `httptest.Server` approach for connector tests requires writing inline
JSON fixtures and handler logic per test case. This is verbose, hard to keep
in sync with real API responses, and makes API drift invisible. A VCR-style
replay approach records real API responses once and replays them in CI without
network access.

## Decision

Add `gopkg.in/dnaeon/go-vcr.v4` as a test dependency. Connector packages
that exercise HTTP paths use VCR cassettes (YAML files committed to the repo
under `testdata/cassettes/`) instead of inline `httptest.Server` handlers.
The shared helper lives in `internal/connectors/vcr/helper.go`.

Cassettes are recorded once against real APIs and replay automatically.
They can be re-recorded when API contracts change
(`VCR_RECORD=1 GITHUB_TOKEN=<token> go test ...`); the committed YAML
captures the exact wire format, making API drift visible.

**Cassette safety.** A `BeforeSaveHook` strips `Authorization` from all
cassette request headers before writing to disk. The URL+method-only matcher
(`matchURLMethod`) ensures cassettes replay correctly even when the header set
changes between recording and replay.

**Mode.** `ModeReplayOnly` by default (CI-safe); `ModeRecordOnly` when
`VCR_RECORD=1`.

## Consequences

**Positive.** Cassettes are recorded once against real APIs. API drift is
visible when cassettes need re-recording. No inline JSON fixture maintenance.

**Negative.** New dependency (`gopkg.in/dnaeon/go-vcr.v4`) added to
`go.mod`; requires an ADR entry per the no-new-dependencies invariant. MIT
license; pure-Go; static binary constraint satisfied.

**Neutral.** `VCR_RECORD=1` re-recording requires a valid token and network
access; CI always runs in replay mode.

## How to apply

Import path: `gopkg.in/dnaeon/go-vcr.v4/pkg/recorder` and
`.../pkg/cassette`. Mode: `ModeReplayOnly` by default; `ModeRecordOnly` when
`VCR_RECORD=1`. First shipped for `internal/connectors/githubactions` (#66);
extend to bugsnag/circleci/honeycomb/sentry when those credentials are
available.
