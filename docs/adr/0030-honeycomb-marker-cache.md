## Status

Accepted. v0.4.4. Closes [#141](https://github.com/kmcd/xray/issues/141).

## Context

The Honeycomb Classic markers API (`GET /1/markers/<dataset>`) has no
server-side date filter — it returns the full marker history every call.
For a dataset with 200k+ entries (typical at mid-sized organisations with
long deployment histories), this produces ~22s of wall-clock time that is
identical across successive `xray run` invocations: the historical marker
set does not change between Tuesday and Wednesday.

The honeycomb leg dominates wall-clock for the class of customers this
hits: 22s of the 31s total in the observed case (70%+ of wall-clock, all
in the markers fetch). This compounds during engagement setup when the
operator iterates repeatedly on `xray check` / `xray run` while tuning
the config.

## Decision

A local disk cache keyed by a SHA-256 fingerprint of `(token, dataset,
baseURL)`. On each `xray run`:

1. Compute `sha256(token + NUL + dataset + NUL + baseURL)`, take the first
   16 hex characters as the fingerprint. The token is never written to disk
   or logged; only the fingerprint appears in the cache filename.
2. Attempt to read `$UserCacheDir/xray/honeycomb/<fingerprint>.json`.
   - Present and `fetched_at` within 24 hours → use cached markers directly;
     skip the HTTP fetch.
   - Absent, corrupt, wrong schema version, or older than 24 hours → fall
     through to a live HTTP fetch.
3. On a successful live fetch, write the response back to disk using an
   atomic temp-file rename (`path+".tmp"` → `path`) with `0o600` permissions.
   Write errors are logged at debug level and do not abort the run.
4. `xray run --no-cache` disables both cache reads and writes, mirroring the
   `--keep-clones` and `--no-run-log` escape hatches.

**TTL policy.** 24 hours. Markers are write-once deploy receipts in practice;
mutation (`PUT /1/markers/<dataset>/<id>`) is rare. Staleness is bounded to
one day, which is within the tolerance of any trend analysis. Operators can
always force a full re-fetch via `--no-cache` or by deleting the cache file.

**Package location.** `internal/connectors/honeycomb/cache.go` — kept
package-local. No new `go.mod` dependency: `crypto/sha256`, `encoding/hex`,
`encoding/json`, `os`, `path/filepath`, and `time` are all stdlib.

**`xray check` is unaffected.** The check command calls `buildConnectors`
with `noCache=false` so it would benefit from the cache on any connector that
then calls `listMarkers`, but `xray check` does not call `Extract`,
so no markers are fetched during a check run regardless.

## Consequences

- **Repeat `xray run` for the same dataset drops from ~22s to <1s after
  the first run.** The full fetch still runs on a cold cache or after 24h.
- **Cache location is `$UserCacheDir/xray/honeycomb/`**, which resolves to
  `~/Library/Caches/xray/honeycomb/` on macOS and `~/.cache/xray/honeycomb/`
  on Linux. Single-user assumption matches xray's overall posture.
- **Marker mutations and deletions are not reflected for up to 24 hours.**
  Acceptable: markers are write-once deploy receipts; the analysis use case
  tolerates a 24h staleness window.
- **`prov.Endpoints["markers"] = {Accessible: true}` on a cache hit.** A cache
  hit returns `(markers, nil)` from `listMarkers` — no HTTP request is made.
  `extract.go` stamps `Accessible: true` based on `err == nil`. This means a
  revoked token with an unchanged token string (same cache fingerprint) reports
  `Accessible: true` for up to 24 hours. Acceptable: the fingerprint includes
  the token, so a *changed* token misses the cache; revocation without a token
  rotation is bounded to the 24h TTL window. `Accessible: true` is interpreted
  as "data was obtained for this endpoint" — from cache or live.
- **Disk footprint is trivial.** 200k markers averaging ~250 bytes per JSON
  line → ~50 MB per dataset. This is the worst-case observed in the field.
- **Token safety.** The token fingerprint (16 hex chars of SHA-256) appears
  in the cache filename. The raw token never touches disk, no cache write
  path logs or includes it, and the test suite asserts the fingerprint does
  not contain the raw token as a substring.
- **Static-binary constraint preserved.** No new `go.mod` entry.
