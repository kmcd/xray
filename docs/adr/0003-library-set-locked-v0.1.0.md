## Status

Accepted. v0.1.0.

## Context

The v0.1.0 build needed a locked library set that was pure-Go (CGO-free,
mandatory per the static-binary constraint), covered the required APIs, and
had a narrow enough surface to be audited.

## Decision

Hard-pin to:

- CLI: `github.com/spf13/cobra`
- TOML: `github.com/BurntSushi/toml` (line-number preservation via `MetaData`)
- SQLite: `modernc.org/sqlite` (pure-Go, CGO-free — mandatory per spec)
- GitHub REST: `github.com/google/go-github/v66`
- GitHub GraphQL: `github.com/shurcooL/githubv4`
- OAuth: `golang.org/x/oauth2`
- Language detection: `github.com/go-enry/go-enry/v2`
- Backoff: `github.com/cenkalti/backoff/v4`
- Logging: stdlib `log/slog`

## Consequences

**Positive.** All pure-Go (CGO-free). Each has the narrowest API surface that
meets the spec.

**Negative.** None identified at lock time.

**Neutral.** `github.com/jackc/pgx`, `mattn/go-sqlite3` (CGO),
`urfave/cli`, and a custom GraphQL client were all rejected.

## How to apply

`go.mod` — any new dependency requires an ADR entry per the
no-new-dependencies invariant in `CLAUDE.md`.
