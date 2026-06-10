# Engagement guide

This document is the consultant-side playbook for using an `xray`
artifact. Audience: a technical consultant running an engagement on
behalf of a customer who has run `xray` themselves and sent the
`.tar.gz` over.

It is the counterpart to [docs/security.md](./security.md) and
[docs/threat-model.md](./threat-model.md), which are written for the
customer. Those documents describe what the artifact contains and what
guarantees the binary makes. This one describes what to do with the
artifact once it arrives.

Tone is procedural. Numbered steps where order matters, bullets where
it does not. The privacy posture — team-level only, no individual
rankings, no application logic read or stored — applies end-to-end
through every section.

## 1. Receiving the artifact

The customer sends two files: the `xray-export-<UTC-timestamp>.tar.gz`
and, optionally, the sibling `.log` file. Before anything else:

1. **Verify the SHA-256 against the line the customer's run printed.**
   Each `xray run` writes a `run: artifact … sha256=<hex>` line at the
   end of the log (see [docs/sample-run.log](./sample-run.log) line 9
   for the format). The customer should send that line in the same
   transmission as the artifact. Recompute locally:

   ```bash
   shasum -a 256 xray-export-*.tar.gz
   ```

   The two hex strings must match exactly. A mismatch means the file
   was modified in transit — do not unpack it; ask the customer to
   resend.

2. **Verify the cosign signature on the binary the customer ran.**
   The artifact carries no signature itself — its trust derives from
   the binary that produced it. Ask the customer for the `xray
   version` output of the binary they ran. Map the `tool_version` to a
   `schema_version` using the table in [README → Compatibility](../README.md#compatibility),
   then verify the binary download against `checksums.txt` and
   `checksums.txt.sig` per [README → Verifying the binary](../README.md#verifying-the-binary).
   The supply-chain detail lives in [docs/threat-model.md → Supply chain](./threat-model.md#supply-chain).

3. **Store the artifact encrypted at rest.** A leaked artifact exposes
   the categories documented in [docs/threat-model.md → What changes if the artifact is leaked](./threat-model.md#what-changes-if-the-artifact-is-leaked):
   repository slugs, commit metadata, team-to-repository mapping. No
   source code, no tokens — the threat model holds — but the metadata
   is still confidential. Use full-disk encryption on the machine that
   holds the working copy.

4. **Agree a retention policy with the customer up front.** Define a
   TTL for the working copy: typically engagement duration plus a short
   post-engagement window for follow-up questions. Delete on schedule
   (see Section 7). Record the deletion date in the engagement
   contract.

## 2. Unpacking and connecting

The artifact is a `.tar.gz` of two files.

1. **Unpack.**

   ```bash
   tar -xzf xray-export-*.tar.gz
   # produces:
   #   manifest.json
   #   metrics.sqlite
   ```

2. **Connect a query tool.** `xray` does not ship an analyser. The
   artifact is a portable SQLite database — point any SQL client at it
   (`sqlite3` from the command line, a desktop SQL client, a Jupyter
   notebook with `pandas.read_sql`, or the engagement's own analyser
   stack). The
   canonical interface is the schema documented in
   [docs/schema.md](./schema.md) at the manifest's `schema_version`.

3. **Run sanity-check counts and compare to the manifest.** For each
   table the manifest's `counts` block lists, `SELECT COUNT(*)`
   against the SQLite must equal the manifest value. Not every DDL
   table appears in `counts` — `pr_review_requests`,
   `branch_protection`, `teams`, `builds`, `build_jobs`, `incidents`,
   `harness_artifacts`, and `repo_file` are present in the schema
   but absent from the counts map; their populations are inferred
   from the corresponding `extraction_provenance.endpoints` entries
   instead. Using the smoke artifact in
   [docs/sample-manifest.json](./sample-manifest.json) as a worked
   example (`schema_version = 2`, `goreleaser/chglog`):

   ```sql
   SELECT 'prs',      COUNT(*) FROM prs       -- expect 64
   UNION ALL
   SELECT 'reviews',  COUNT(*) FROM reviews   -- expect 56
   UNION ALL
   SELECT 'commits',  COUNT(*) FROM commits   -- expect 65
   UNION ALL
   SELECT 'defects',  COUNT(*) FROM defects   -- expect 309
   UNION ALL
   SELECT 'deploys',  COUNT(*) FROM deploys;  -- expect 6
   ```

   A row count that disagrees with `manifest.counts` is a corruption
   signal, not a connector quirk — investigate before reading further.

4. **Confirm the `_schema` row.** A single-row `_schema` table records
   the `schema_version`, the `tool_version` of the binary that
   produced the artifact, and `applied_at` (the RFC3339 timestamp the
   row was written). Cross-check `tool_version` against the manifest
   and the `xray version` string the customer sent. Disagreement
   means the artifact was edited.

## 3. Reading the manifest

The manifest is the run summary. Every interpretation downstream
depends on reading it correctly. The block that does the most work is
`extraction_provenance` — one entry per repo × connector, recording
which endpoints were reachable and what came back.

- **The unknown-vs-zero rule.** An endpoint with `accessible: false`
  means *unknown*. It does not mean *no signal*. A repository whose
  `branch_protection` endpoint records `accessible: false` is **not**
  a repository without branch protection — it is a repository where
  the token lacked admin permission to check. Treat the column as
  `NULL`, not `0`. The same logic applies across every
  permission-gated endpoint listed in
  [docs/security.md → Network surface](./security.md#6-network-surface).

- **Per-row error inventory.** The `errors` map on each provenance
  entry records per-row failures the connector continued past. A
  small map is expected; large ones, or recurring errors against the
  same endpoint, mean the data for that endpoint is incomplete in a
  patterned way. The fix is to ask the customer to re-run with the
  underlying cause addressed (token scope, rate-limit budget,
  provider-side downtime).

- **`pagination_complete: false`.** The walk stopped before reaching
  the end of the result set, typically because the per-request
  rate-limit budget exhausted. The collected rows are valid; the tail
  is missing. Two paths: scope claims to the covered range and note
  the truncation, or ask the customer to re-run with a larger
  budget. The bigger the missing tail, the more important the re-run.

- **`endpoints` map.** Each connector lists every endpoint it tried
  with `accessible` and an optional `reason` when not. A repository
  whose `codeowners` endpoint records `accessible: false, reason:
  "404"` is a repository without a CODEOWNERS file — not the same as
  a 403. Read the reason before drawing conclusions.

- **`flags` block.** `mailmap_applied` is the load-bearing one for
  authorship analysis. When it is `false`, any metric that depends on
  collapsing aliases (truck factor, main-author share, Conway's-law
  signals) is inflated. Surface the caveat in the report.

- **Honeycomb is dataset-scoped.** Honeycomb has no per-repo concept; all
  deploys and SLO-alert incidents are emitted under one repo slug — whichever
  repo was processed first (the order is not guaranteed). For every other repo
  the `markers` endpoint records `accessible: false` with reason `"honeycomb
  has no per-repo concept; emitted under <slug>"`: this is expected behaviour,
  not a permission error.

## 4. Per-table analysis recipes

Four DORA-adjacent recipes against `schema_version = 2`. Each lists
the SQL, the meaning, and the common pitfalls. Numbers shown are
real, from
`/private/tmp/xray-smoke-chglog/extracted-fresh/metrics.sqlite` (the
`goreleaser/chglog` smoke artifact).

### Lead time to change

```sql
WITH lt AS (
  SELECT (julianday(merged_at) - julianday(opened_at)) * 24.0 AS hours,
         ROW_NUMBER() OVER (
           ORDER BY julianday(merged_at) - julianday(opened_at)
         ) AS rn,
         COUNT(*) OVER () AS n
  FROM prs
  WHERE merged_at IS NOT NULL
)
SELECT ROUND(hours, 2) AS lower_median_hours
FROM lt
WHERE rn = (n + 1) / 2;
```

**Meaning.** Lower median of the wall-clock interval between opening
and merging a PR (the `(n+1)/2`-th row in sorted order — exact median
on odd `n`, the lower of the two centre rows on even `n`). The
`ORDER BY julianday(...)` matters: ordering by `merged_at -
opened_at` directly would coerce the ISO-8601 TEXT columns to numbers
via their leading digits and collapse same-year intervals to zero.
The smoke artifact returns `0.08` hours against `chglog`, a
release-please automated repo where most PRs merge within minutes.

**Pitfalls.** Draft PRs distort the start time. When
`ready_for_review_at` is populated (see [docs/schema.md → `prs`](./schema.md#prs)),
use it instead of `opened_at`. Squash-merge automation can collapse
many human-PR cycles into one machine PR — read `n_squash_merged_prs`
and `n_total_merged_prs` in the manifest to spot this before drawing
team-level conclusions. The `0.08` figure shown is representative
only — regenerate it from the live smoke artifact when the smoke
target moves.

### Defect-linked merged PRs (a proxy, **not** DORA change-failure rate)

```sql
WITH defect_pr AS (
  SELECT DISTINCT
    d.repo,
    CAST(
      SUBSTR(
        d.id,
        INSTR(d.id, ':') + LENGTH(d.source) + 2,
        INSTR(
          SUBSTR(d.id, INSTR(d.id, ':') + LENGTH(d.source) + 2),
          ':'
        ) - 1
      ) AS INTEGER
    ) AS pr_number
  FROM defects d
  WHERE d.source IN ('pr_body', 'pr_title')
)
SELECT
  COUNT(DISTINCT p.number)                                 AS prs_with_defect_ref,
  (SELECT COUNT(*) FROM prs WHERE merged_at IS NOT NULL)   AS merged_total
FROM defect_pr dp
JOIN prs p ON p.repo = dp.repo
           AND p.number = dp.pr_number
           AND p.merged_at IS NOT NULL;
```

**Meaning.** Count of merged PRs whose title or body references a
ticket the `defects` table recognises, over total merged PRs in the
window. This is **not** the DORA change-failure rate
(failed-deploys / total-deploys) — it is a ticket-linkage proxy,
useful only as a starting point for the conversation. The smoke
artifact returns `49 / 61` against `chglog`, where most PRs link
release-note tickets in their body, so the ratio over-counts. The
SUBSTR/INSTR expression parses the third `:`-separated field of
`defects.id` (`repo:source:scope_id:ref`); the `scope_id` is the PR
number for `pr_body` and `pr_title` rows.

**Pitfalls.** `defects` is a text-reference table, not a ticket-system
integration (per the v1 non-goals in [CLAUDE.md](../CLAUDE.md)). A PR
that references `#1234` in title or body is recorded; a PR that fixes
a customer-side bug closed in a separate tracker is invisible. The
recipe captures both `pr_body` and `pr_title` `defects.source`
entries — repos using Conventional-Commits-style PR titles
(`fix(PROJ-123): ...`) would otherwise miss the title reference.
Always pair the result with a conversation about the customer's
actual defect
process before reporting the number. The `49 / 61` figure is
representative; regenerate from the live smoke artifact when the
target moves.

### Deploy frequency

```sql
SELECT strftime('%Y-%W', deployed_at) AS year_week,
       environment,
       COUNT(*) AS deploys
FROM deploys
GROUP BY year_week, environment
ORDER BY year_week;
```

**Meaning.** Deploys per environment per week. The smoke artifact has
six deploys, all sourced from GitHub Releases — the `environment`
column is empty because GitHub release-tag deploys do not carry an
environment label.

**Pitfalls.** The Honeycomb attribution caveat
(see [docs/schema.md → `deploys`](./schema.md#deploys)) — Honeycomb
has no per-repo concept, so `(repo, source = 'honeycomb')` rows
attribute to the alphabetically-first configured repo. Treat the
attribution as approximate. Empty `environment` is the GitHub-release
case, not a data bug.

### Review latency

```sql
WITH first_review AS (
  SELECT repo, pr_number, MIN(submitted_at) AS first_at
  FROM reviews
  GROUP BY repo, pr_number
),
first_request AS (
  SELECT repo, pr_number, MIN(requested_at) AS req_at
  FROM pr_review_requests
  GROUP BY repo, pr_number
)
SELECT COUNT(*) AS prs_with_both,
       ROUND(AVG((julianday(first_at) - julianday(req_at)) * 24.0), 2)
         AS avg_hours
FROM first_review fr
JOIN first_request rq USING (repo, pr_number);
```

**Meaning.** Average wall-clock hours between requesting a reviewer
and receiving the first review. The query returns one row with
`prs_with_both = 0` and `avg_hours = NULL` against the smoke artifact
because `pr_review_requests` is empty for `chglog`. `pr_review_requests`
is populated from the GitHub timeline
(see [docs/schema.md → `pr_review_requests`](./schema.md#pr_review_requests)),
which records `ReviewRequested` / `ReviewRequestRemoved` events; an
empty table means no such events were emitted in the window.

**Pitfalls.** Repos that rely exclusively on CODEOWNERS for implicit
reviewer assignment (no timeline event), or PRs reviewed without
any request event at all, leave `pr_review_requests` empty. The
query can also return a negative `avg_hours` if a reviewer submits a
review before any `ReviewRequested` event lands — guard against that
in downstream code or filter `req_at < first_at` in the JOIN. When
the table is empty, fall back to `prs.opened_at → reviews.submitted_at`
as a coarser proxy and label it as such in the report.

### A note on empty tables

The smoke artifact has `incidents = 0` and `builds = 0` because
`goreleaser/chglog` does not configure Sentry / Bugsnag / Honeycomb
and has no Actions runs in the extraction window. Empty does not
imply absent. Check `extraction_provenance.endpoints` and
`connectors_used` in the manifest. If the connector was not
configured, the customer's environment has no signal for it; if the
connector was configured and rows are still zero, the absence is
genuine or the data is unknown — Section 3's unknown-vs-zero rule
applies.

## 5. Recommendations framework

The job is to turn extracted signal into recommendations a
customer's engineering organisation can act on. The signal does not
do that work on its own.

- **Signal → question → recommendation.** A trend, on its own, is a
  question, not a recommendation. *"Lead time has doubled over six
  months"* is a question. *"Lead time has doubled because senior-review
  capacity on the smallest team is concentrated on a single role and
  PRs sit two days waiting on that role; we recommend redistributing
  senior-review responsibility across the team"* is a recommendation.
  The recommendation requires the signal **and** a causal story the
  customer can verify or refute, framed at the role / process level
  rather than around a single person.

- **Team-level only.** No per-individual recommendations, no
  individual rankings, no per-individual aggregations — even if the
  customer asks. This is not a discretionary policy; the schema
  enforces it (see [docs/security.md → Per-individual data](./security.md#4-per-individual-data)
  and the schema rules around `*_handle` opacity in
  [docs/schema.md → Author handles](./schema.md#author-handles-_handle-columns)).
  Recommendations describe team-level systems and processes.

- **Confidence calibration.** Downgrade the strength of a claim when
  the data supporting it is partial or unknown:
  - An endpoint with `accessible: false` on the signal's path →
    downgrade from recommendation to question.
  - `pagination_complete: false` on the signal's path → scope the
    claim to the covered range or ask the customer to re-run.
  - `mailmap_applied: false` plus an authorship-derived signal →
    flag the inflation explicitly in the report; do not bury it.
  - A small *n* (few merged PRs, few deploys) → present the number,
    say it is small, and avoid grand conclusions.

- **Read the threat model into every recommendation.** The artifact
  excludes source content and individual identities by construction
  ([docs/security.md → No source content stored](./security.md#2-no-source-content-stored)
  and [§ Per-individual data](./security.md#4-per-individual-data));
  the recommendation layer must not re-introduce them. A
  recommendation that names a specific contributor, infers identity
  from a unique role, or pairs a `*_handle` to a real name is out of
  bounds regardless of framing.

## 6. Sending findings back to the customer

The artifact stays inside the consultant's environment. Only the
synthesis goes back. The synthesis is a written report, not a
database dump.

**Suggested report structure.**

1. Executive summary — three to five sentences the customer's
   engineering leadership reads first.
2. Per-team observations — what the data shows, team by team, joined
   through the `teams` table.
3. Cross-team patterns — what shows up across teams; what does not.
4. Recommendations — each one carrying a confidence band (high /
   medium / low) keyed to Section 5.
5. Data caveats — every `accessible: false`, every
   `pagination_complete: false`, every `mailmap_applied: false`,
   stated up front so the customer can weigh the report against
   them.

**Never share back.** The list is short and load-bearing:

- The `metrics.sqlite` itself. The customer already has the data
  needed to produce it; the consultant's copy is a derived work and
  belongs in the engagement boundary.
- Raw row dumps in any form. Aggregate every number to at least the
  team level before it appears in the report.
- Per-`*_handle` tables, lists, charts, or attributions. The
  `*_handle` strings are opaque; the customer's local mapping back
  to identities defeats the privacy posture.
- Anything that could reveal individual contribution patterns —
  "the contributor with the most reviews", "the highest-volume
  reviewer", "the on-call who pages most" — even if the customer
  knows the identity already. The principle is no per-individual
  data, not no surprises.

**Privacy posture under customer pressure.** Customers occasionally
push for more detail than the artifact supports: individual
rankings, named-contributor charts, "who is the bottleneck". Decline
and explain the opacity. The `*_handle` strings are one-way hashes;
the schema explicitly omits per-individual aggregations
([docs/security.md → Per-individual data](./security.md#4-per-individual-data));
the analyser contract (the `schema_version` integer in the
manifest) rejects any artifact that introduces per-individual
aggregation tables, so the data path itself does not support what
the customer is asking for. Restating the threat model in the
customer's terms is the answer.

## 7. End of engagement

The engagement closes when the report is delivered and the customer
has had a window for follow-up questions. Close it cleanly.

- **Consultant-side: delete the working copy.** Remove the unpacked
  `metrics.sqlite`, the `manifest.json`, the original `.tar.gz`, any
  derived notebooks or extracts. Confirm deletion in writing to the
  customer. The deletion date should match the TTL agreed in
  Section 1.

- **Customer-side cleanup (reminder for the customer).** The
  customer holds artifacts the consultant never received: their
  `xray.toml` (with connector tokens), any temp clone directories
  if they ran with `--keep-clones`, the sibling `.log` file. Remind
  them to remove these on their own schedule. None of these reach
  the consultant under normal operation, but the reminder closes
  the loop.

- **Token rotation.** Recommend that the customer rotate or revoke
  the token used during extraction at engagement close, regardless of
  whether it was issued specifically for `xray` or borrowed from an
  existing credential. `xray`'s read-only guarantee
  ([docs/security.md → Read-only](./security.md#1-read-only))
  addresses what the tool did with the token — not whether the token
  was exposed on the operator's machine, in shell history, in a
  process list, or to any other software running there. Rotation is
  a defence-in-depth control against exposure surface, not a
  statement about `xray`'s behaviour.

- **Engagement-side records.** The report stays with the engagement.
  The customer keeps a copy. The consultant retains their own copy
  according to the engagement contract — typically for the duration
  of the contractual record-keeping window, then deleted.

## See also

- [docs/security.md](./security.md) — what the artifact contains and
  what the binary guarantees (customer-facing).
- [docs/enterprise.md](./enterprise.md) — running xray behind a
  corporate proxy, custom CA, or allowlist firewall (customer-facing).
- [docs/threat-model.md](./threat-model.md) — trust boundaries and
  attack surface (customer-facing).
- [docs/schema.md](./schema.md) — canonical table definitions, the
  interface contract for any analyser.
- [docs/spec.md](./spec.md) — full command surface, config schema,
  artifact shape.
- [docs/sample-manifest.json](./sample-manifest.json),
  [docs/sample-run.log](./sample-run.log) — the example artifact the
  recipes in Section 4 are tested against.
