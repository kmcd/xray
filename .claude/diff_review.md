# Diff review criteria

Apply these criteria when reviewing any code change in xray. Not all sections apply to every diff — use judgement.

## Schema and DDL

- **`internal/model/types.go` parity** — every new column in the DDL has a matching field in the Go struct, with the same nullability semantics. `*time.Time` ↔ `TEXT NULL`. `*int` / `*bool` / `*float64` ↔ `INTEGER NULL` / `REAL NULL`.
- **schema_version bump** — when a column is removed or renamed, when a column's type or semantics change, or when a value is removed from a `source` enum. Adding tables, adding nullable columns, or adding enum values is non-breaking and does **not** bump.
- **Index coverage** — every join or filter pattern the analyser will run has an index. `(repo, ...)` is the conventional prefix for analyser queries.
- **Primary keys** — composite where the model demands uniqueness (`(repo, sha)`, `(repo, source, id)`, `(repo, pr_number, label)`). Never invent a synthetic ID where the natural key is already unique.
- **`docs/schema.md` updated** — every column change has a matching row in the schema reference.

## Connector contract

- **Read-only** — no `POST`, `PATCH`, `PUT`, `DELETE` calls. Audit imports of any go-github method whose name starts with `Create`, `Update`, `Delete`, `Edit`, `Add`, `Remove`. If you find one, that's a contract break.
- **Provenance** — every successful row insert increments `prov.RowsReturned[<table>]`. Every error appends to `prov.Errors[<context>]` and continues. Pagination interruptions set `prov.PaginationComplete = false`. Rate-limit truncation sets `prov.RateLimitTruncated = true`.
- **Permission-gated endpoints** — 403/404 on an endpoint sets `prov.Endpoints[<endpoint>] = EndpointStatus{Accessible: false, Reason: "..."}` and skips rows for that endpoint. The analyser reads absence-because-inaccessible as **unknown**, not **no signal**.
- **Window filtering** — every row's timestamp is checked against `window.Contains(...)` before insertion. Skipping is silent (no error); it's not a failure.
- **Body discipline** — PR bodies, commit-message bodies, review-comment bodies are parsed at extract time, contribute structured columns (lengths, counts, marker flags), and are **never** persisted. If a body is held in a variable, it must drop out of scope inside the same function.

## HTTP boundaries

- **Token never logged** — grep your diff for any `slog.String("token", ...)`. If a connector exposes a `Token()` accessor, that's a smell.
- **ratelimit.Transport wrapping** — every connector's HTTP client has its `Transport` wrapped with `&ratelimit.Transport{Base: ..., Policy: ratelimit.DefaultPolicy(), Log: log}`. For oauth2-wrapped clients, the wrap goes on `oauth2.Transport.Base`, not on top — otherwise retries don't see the token.
- **Context propagation** — every API call takes `ctx` as the first argument and honours `ctx.Done()` while paginating. Long loops poll `ctx.Err()` between pages.
- **4xx vs 5xx** — 429 retries; other 4xx is permanent failure with no retry. 5xx retries up to the policy budget. This is enforced by `ratelimit.Do`; connectors that build their own retry path are a smell.

## File IO

- **Per-run temp dir** — every clone, intermediate file, and the SQLite database live under `os.MkdirTemp("", "xray-<run_id>-")`. Nothing is written to the user's working dir except the final artifact at `Options.Out`.
- **No source content stored** — `commit_files` is numstat only, `file_metrics` is byte-scan stats only, `harness_artifacts.content` is empty unless `capture_harness_content = true`.
- **`#nosec` annotations** — every `// #nosec` carries a one-line justification naming **why** the flagged operation is safe in this context.

## Tests

- **Pure-function mappers** — JSON-to-row mapping logic lives in pure functions and is table-driven. Don't mock HTTP if you can shape the input directly.
- **Connector tests at the boundary** — when HTTP is unavoidable, use `httptest.NewServer` and override the connector's `baseURL`. No test should hit a real provider.
- **Fixture stability** — date-relative test data uses fixed `time.Time` constructors; never `time.Now()` in a fixture.
- **No body content in tests** — fixture PR/commit bodies are short strings checked for length and markers, never for their actual text being preserved.

## Same-class scan

- **Shape, not symbol.** Every bug or feature in this diff has an abstract shape — *"error path that should write to multiple provenance keys but only writes to one,"* *"permission-gated endpoint missing `EndpointStatus`,"* *"byte-size formatting drift between adjacent customer outputs."* Name the shape in one sentence before reviewing.
- **grep for peers.** Search the codebase for the shape. Use real identifiers (sibling connector names, sibling model fields); do not rely on memory. The grep is what makes the scan honest.
- **Peer handling.** Every peer found has a verdict: *(a)* fixed in this diff, *(b)* filed as one class-level issue covering the remaining N sites, or *(c)* ignored with a reason in the PR body. Filing N instance-level follow-up issues for the same class is a smell — collapse to one.
- **No new bugs of an existing class.** If the diff introduces a new instance of a class already named in `CLAUDE.md` ("Non-negotiable invariants") or `docs/spec.md`, that is a contract break — fix it or surface it explicitly.

## Style and discipline

- **Concise English commit messages** — imperative subject, no Claude co-author trailer, no emojis.
- **No new dependencies without an ADR entry** — the pinned set in `go.mod` is the contract.
- **No package-comment duplication** — packages with `doc.go` keep their package comment there; new files do not carry `// Package …` comments.
- **Errors wrapped at the boundary** — `fmt.Errorf("connector: %w", err)` at the connector entry. Internal helpers can bubble.
- **No `interface{}`** — use `any`. `time.Time` not `int64` for timestamps. `*time.Time` for nullable.

## Tooling and gates

- **CI must mirror local** — every gate that fails on `make gates` must also fail in CI. If you exclude a lint locally, exclude it in `.golangci.yml`, not only in your invocation.
- **Coverage thresholds** — keep `.testcoverage.yml` permissive at v0.x; tighten once the connector test surface stabilises.
- **govulncheck** — bumping the `go` directive in `go.mod` is the canonical fix for stdlib vulnerabilities. Connector-side vulns get an indirect upgrade via `go mod tidy`.
