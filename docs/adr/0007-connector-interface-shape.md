## Status

Accepted. v0.1.0.

## Context

The connector interface needed to be defined in a way that let the compiler
enforce the canonical model, made provenance a first-class concern, and kept
the API minimal.

## Decision

Connectors expose three methods: `Name()`, `Ping(ctx)`,
`Extract(ctx, repo, window, sink) Provenance`. Sinks are typed (one method
per canonical table) rather than generic `Insert(row)`. Provenance is the
return value, not a side channel.

## Consequences

**Positive.** Typed sinks let the compiler enforce the canonical model — a
connector cannot accidentally invent a table. Provenance-as-return makes the
"did this connector cover the window?" question first-class for the manifest
writer.

**Negative.** Adding a table is a Sink interface change (every connector
recompiles). Acceptable at this stage; the table set is the schema.

**Neutral.** See ADR 025 for the optional `Prefetcher` extension to this
interface.

## How to apply

`internal/connector/connector.go` — the canonical interface definition.
Connectors in `internal/connectors/*/` implement it.
