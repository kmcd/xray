# Agent dispatch template

Verbatim clauses to paste into agent prompts when fanning out parallel work. Each clause exists because of a specific past failure during the v0.1.0 build-out. Quote verbatim; do not paraphrase.

## Always include

### Scope-pinning clause

> Working dir: `/Users/kmcd/src/xray` (Go module `github.com/kmcd/xray`, Go 1.26.4). Pinned deps in `go.mod`. **Do not modify** `go.mod`, `go.sum`, `.github/**`, `README.md`, `CLAUDE.md`, `Makefile`, `.goreleaser.yaml`, `.golangci.yml`, `.gitignore`, `tmp/**`, or any package outside the directories listed in your "What to build" section. If you find yourself needing to edit a forbidden path, stop and surface the dependency in your deliverable instead.

**Why:** The M1/M2 agents collided on shared files; pre-pinning prevents `go.mod` churn and unintended foundation edits.

### Foundation-types clause

> The following are pre-defined and must **not** be redefined or modified by you:
>
> - `internal/connector/connector.go`, `sink.go` — Connector + Sink contracts
> - `internal/model/types.go`, `schema.go` — row structs and DDL
> - `internal/config/types.go` — Config struct
>
> Read them and consume them; don't rewrite them.

**Why:** Lost an hour the first time when `internal/config/types.go` was never committed because two agents both assumed the other had it. The fix: declare foundation types as "already here, read them" in every brief.

### Idiom-anchor clause

> Before adding code to package `P`, read at least one well-established sibling file in `P` to anchor style, error handling, logging-level discipline, and table-driven test shape. Match the established style; don't drift. If `P` is sparse or you're scaffolding a new package, the canonical references are:
>
> - `internal/connectors/github/extract.go`, `prs.go`, `commits.go` — connector idioms (extract orchestration, provenance writes, paginator shape, GraphQL + REST mixing).
> - `internal/store/store.go` — store idioms (mutex-guarded inserts, prepared statements, `nstr` / `nbool` / `rfc` null-helpers, append-only `InsertX` shape).
> - `internal/connectors/github/extract_test.go`, `prs_eof_retry_test.go` — connector test idioms (table-driven shape, `httptest.NewServer` fixtures, `recordingSink`, fixture-stable timestamps).
> - `internal/ratelimit/ratelimit.go` — transport idioms (atomic-load hot path, multi-line comments capturing intent, CAS-loop discipline on contended state).
> - `internal/connectors/github/walk.go` — local-IO idioms (working-tree walks, vendor/binary pre-classification, sharded fan-out).
>
> Cite the file(s) you anchored on in your deliverable so review can verify the match.

**Why:** Connector, store, and ratelimit packages have grown idioms not documented elsewhere — provenance error-recording shape, mutex granularity, multi-line comment density on intent, table-driven test layouts. Agents adding to a sparse package (a new connector, a fresh store helper) tend to invent a slightly different convention each time, which fragments the codebase. Anchoring on a canonical reference once per session keeps drift bounded and surfaces during review as a citation rather than a diff diff.

### Git-handoff clause

> **Do not** run `git`. Do not stage, commit, push, tag, or reset. Main commits after collecting your deliverable. Report the file list and per-issue commit message — no Claude co-author trailer, concise imperative subject, body only when not self-evident from the diff.

**Why:** Parallel agents racing on the git index would corrupt it. Also keeps commit messages consistent in style.

### Dependency clause

> Do not add dependencies. The pinned set is in `go.mod`. If your work needs a new library, surface the dependency in your deliverable instead of adding it; main will decide.

**Why:** `go mod tidy` from a per-agent process pollutes the indirect set and races against parallel agents.

### Deliverable clause

> When done, reply with:
>
> 1. Files created or modified, grouped by which issue closes it (`#NN`).
> 2. A one-line suggested commit message per issue (imperative subject, no trailer).
> 3. Any deviation from the spec or anything you couldn't implement, with a one-line reason.
> 4. Any assumption you made about a parallel agent's output that needs to be honoured at commit time.

**Why:** Item 4 caught the github connector's `SetCaptureHarnessContent` setter assumption and the JSON-tag fix on `connector.Window`. Without it those would have been latent.

## Include when the scope hits HTTP

### Read-only clause

> Connectors must be read-only. Never call a write/mutate endpoint — no `POST`, `PATCH`, `PUT`, or `DELETE`. If you find yourself reaching for `Create*`, `Update*`, `Delete*`, `Edit*`, or `Add*` methods on any SDK, stop. This is asserted at code review and is non-negotiable per the spec.

### Ratelimit clause

> Every HTTP client must wrap its `Transport` with `&ratelimit.Transport{Base: <existing transport>, Policy: ratelimit.DefaultPolicy(), Log: log}`. For oauth2-wrapped clients, the wrap goes on `oauth2.Transport.Base` so retries happen **after** the token is attached.

### Provenance clause

> Every successful row insert increments `prov.RowsReturned[<table>]`. Every error appends one summary entry to `prov.Errors[<context>]` and continues — a per-row error does not abort the connector. Pagination interruptions set `prov.PaginationComplete = false`. Endpoints that return 403/404 record `prov.Endpoints[<endpoint>] = connector.EndpointStatus{Accessible: false, Reason: "..."}` and skip rows for that endpoint.

### Body-discipline clause

> PR bodies, commit-message bodies, review-comment bodies, and any other free-text fields are parsed at extract time, contribute structured columns (lengths, counts, marker flags), and are **never** persisted. The body variable must drop out of scope inside the same function. If a downstream pass needs a signal, derive a structured column for it now.

## Include when the scope touches the schema

### Schema-parity clause

> If you add a column to `internal/model/types.go`, you must also add it to `internal/model/schema.go` (the DDL) and `docs/schema.md` (the reference). Adding a column with a default value is non-breaking; `schema_version` does **not** bump. Removing, renaming, or changing the semantics of a column **does** bump it.

### Sink-method clause

> If you add a Sink method, declare it on `connector.Sink` in `internal/connector/sink.go` and implement it on `*store.Store` in `internal/store/store.go`. Both must change in the same commit or neither.

## Include when multiple agents share a package

### Forward-reference clause

> A parallel agent will land additional files in this same package. Calling a function whose definition lives in their file is fine — the package compiles together. List every assumption you make about their work (function signatures, struct fields you reference) at the bottom of your deliverable.

**Why:** The github connector was split between agents M3 and M4; declaring forward references explicitly let both write in parallel without coordination overhead.

## Picking clauses

Read the issue scope. Decide which families apply. Quote verbatim. Do not summarise. Do not paraphrase.

The cost of including an irrelevant clause is small (a few extra paragraphs the agent reads once). The cost of omitting a relevant one is the failure mode that originally motivated it — and those have all already cost an hour each.
