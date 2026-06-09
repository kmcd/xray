## Status

Accepted. v0.3.x. Closes [#98](https://github.com/kmcd/xray/issues/98).

## Context

Issues [#87–#95](https://github.com/kmcd/xray/issues/87) surfaced repeated
provenance-accounting invariant violations that the `make gates` pipeline
did not catch: missing `prov.PaginationComplete = false`,
`prov.RowsReturned[<table>]++`, or `EndpointStatus{Accessible: false}`
writes. Each shipped silently. The audit pattern was always the same — a
human reading the diff against `CLAUDE.md` "Non-negotiable invariants",
spotting the gap, opening an issue, fixing inline.

The fix doctrine works but doesn't scale. As the connector surface
stabilises, the audit needs an automated counterpart that fires on the
same invariant shape without requiring a human to look every time.

## Decision

Three sensor layers, each at a different cadence, mapped to the shape
`xray` actually is (HTTP-heavy, invariant-driven, single-static-binary):

**Layer 1 — Gate-time linters (every `make gates`).** Eight additions to
`.golangci.yml`: `bodyclose`, `noctx`, `errorlint`, `unconvert`,
`wastedassign`, `prealloc`, `usestdlibvars`, `depguard`. `depguard`
encodes the core/connector seam from CLAUDE.md as a lint-time rule
(`internal/connector/**` may not import `internal/connectors/...`).
Same threshold doctrine as the existing practitioner-named sensors
(`funlen`/`gocognit`/`gocyclo`/`nestif`/`dupl`): tuned above v0.2.0
baseline so the gate fires on regression, not on existing surface.

**Layer 2 — Manual sweep (`make sweep`, quarterly).** Three tools too
noisy or slow for CI: `deadcode`, `nilaway`, `gocritic` (via
`golangci-lint --default=none --enable=gocritic`). Findings get triaged
by hand; trivial fixes land inline, substantive ones become follow-up
issues. Captured in `tmp/sweep-findings.md` for each run.

**Layer 3 — Mutation audit (`make mutation-audit`, every release that
touches a connector).** [`gremlins`](https://github.com/go-gremlins/gremlins)
against the provenance-emitting paths. Surviving mutants on
`prov.PaginationComplete` / `prov.RowsReturned[<k>]` / `prov.Errors[<k>]`
/ `EndpointStatus` writes get a killing test in the same PR. Pure-Go,
no native deps, fits the static-binary constraint.

The three layers cover complementary failure modes: lint catches missing
seams (`noctx`, `depguard`) and obvious unsafe patterns
(`bodyclose`); the sweep catches dead code and unsound nil flow that
needs human triage; gremlins catches "the test asserted rows arrived but
nobody asserted the counter incremented".

### A. gremlins scope — invariant-emitting paths only

A module-wide gremlins run is expensive (~tens of minutes per package on
hot paths). The audit scopes to packages that emit provenance: the
contract package (`internal/connector/`) plus the connector subpackages
with `provenance_test.go` files (`github`, `githubactions`, `bugsnag`,
`honeycomb`). `sentry` and `circleci` are excluded until VCR cassettes
land for them (#66 follow-ups) — without test cassettes every mutation
trivially survives, biasing the audit.

### B. gremlins config — single worker, generous timeout

`.gremlins.yaml` sets `workers: 1` and `timeout-coefficient: 5`. The
default parallel workers produce non-deterministic killed/lived counts
across runs (same mutant flips between LIVED and KILLED depending on
which goroutine wins the test-run race). Determinism outweighs
wall-clock for an audit whose output is a triage list. The generous
timeout accommodates HTTP-stub tests under mutation pressure (extra
retry loops). gremlins itself has no path-include config; scope is
provided as positional args to `gremlins unleash` from the Makefile
target.

### C. depguard `files:` pattern needs trailing `/*`

depguard's `pkg:` field already matches by path prefix in `list-mode:
lax`, so the bare parent
`"github.com/kmcd/xray/internal/connectors"` denies every concrete
subpackage (`/github`, `/sentry`, …). What was *not* obvious: the
`files:` glob `"**/internal/connector/**"` matches directories but not
files in depguard's gobwas/glob implementation, so the rule silently
doesn't fire even though `pkg:` is right. The working form is two
patterns — `"**/internal/connector/*"` for top-level files and
`"**/internal/connector/**/*"` for nested ones — verified by importing
a concrete connector under a leaf file in `internal/connector/` and
observing depguard fire. Diagnostic note: golangci-lint dedupes
findings at the same source position, so a *blank* import test gets
masked by revive's `blank-imports` rule; a non-blank import is the
reliable verifier.

### D. gocritic categories — tag, not category

The original issue described "experimental + performance categories";
in golangci-lint v2, `experimental` is a **tag**, not a category. The
three categories are `diagnostic`, `style`, `performance`. `make sweep`
runs `golangci-lint run --default=none --enable=gocritic` which picks
up all three with their default tag selections; the audit captures
findings in `tmp/sweep-findings.md` rather than gating on them.

## Out of scope

- *Adding `gocritic`, `deadcode`, `nilaway`, or `gremlins` to `make
  gates`.* All three would either flag substantively pre-existing code
  (nilaway false positives on map invariants, gocritic on intentional
  patterns) or extend the gate's wall-clock by minutes (gremlins). The
  audit cadence is the right scope.
- *Module-wide gremlins run.* Explicitly bounded to provenance-emitting
  paths.
- *Re-running the manual sweep more than quarterly.* Most findings are
  pre-existing surface that doesn't change much between releases; once
  triaged, they don't re-surface without a substantial refactor that
  would also trigger a gates-level signal.
- *Adopting `wrapcheck`, `gochecknoinits`, `go-cleanarch`, or
  `goheader` from the original issue's skip-list.* Each was discounted
  in the issue for specific reasons (noise floor, irrelevance to the
  codebase, premature DDD enforcement).

## How to apply

`.golangci.yml` — add 8 linters; add `depguard.rules.core-no-connectors`
with both bare-parent and `/**` deny patterns; tune any new-linter
findings inline (one inline fix in `internal/run/run_test.go` for
`errorlint`; `noctx` exclusions for `internal/store/store.go` and the
DB-heavy `internal/model/schema_test.go` / `internal/postprocess/postprocess_test.go`
where threading ctx through every call buys no behavioural change).

`Makefile` — add `sweep` and `mutation-audit` targets, separate from
`gates`. Each target's install commands live in adjacent comments.

`.gremlins.yaml` (new) — `workers: 1`, `timeout-coefficient: 5`, default
mutator set.

`internal/connector/connector_test.go` — add
`TestProvenance_Merge_GraphQLPoints` to kill mutants on the `Merge`
GraphQL points logic that no existing test exercised.

`internal/connectors/github/*_test.go` — add `RowsReturned[k]`
assertions to existing extract tests where the test already inserts the
matching rows; surfaces a `++ → --` mutation flip on the increment that
sink-shape assertions alone don't catch.

`internal/ratelimit/ratelimit_test.go` —
`TestBudgetExceededDoesNotWrapLastErr` pins the deliberate `%v` (not
`%w`) at `ratelimit.go:193` so future `errorlint` "fixes" can't silently
make a budget-exhausted error unwrap as `context.Canceled` and misroute
to exit 130.

Cadence: layer 1 every push; layer 2 once per quarter (recorded in
`tmp/sweep-findings.md`); layer 3 once per release that touches a
connector (recorded in `tmp/mutation-report.md`).
