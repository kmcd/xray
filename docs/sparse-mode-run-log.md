Reference for the log output produced by `xray run` when sparse-historical PR
sampling is active. It covers the log lines specific to sparse mode alongside
the standard run messages you will see in every extraction.

All log lines use structured key=value format. The examples below are taken
from real runs or directly from the source.

---

## What sparse mode is

Sparse mode splits PR extraction into two slices when your config contains
`pr_inflection`:

- **Full-fidelity slice** — from `(pr_inflection − pr_bracket_window)` through
  `window.end`. Every PR in this range is extracted.
- **Pre-bracket slice** — from `window.start` through `(pr_inflection −
  pr_bracket_window)`. When `pr_history_sample` is set, xray samples N PRs per
  calendar-month bucket from GitHub's search API. When `pr_history_sample` is
  omitted, the pre-bracket period is skipped entirely.

The three config keys that activate sparse mode are:

```toml
[connectors.github]
token = "ghp_..."
pr_inflection     = "2023-06-01"   # operator-supplied date from CTO interview
pr_bracket_window = "12m"          # full-fidelity window on each side
pr_history_sample = "monthly:20"   # 20 PRs per month; append :random for random sample
```

`pr_inflection` and `pr_bracket_window` are mutually exclusive with
`pr_window`. Validation rejects configurations that set both.

---

## Standard run start messages

These appear at the top of every log regardless of mode.

```
time=2026-06-08T17:02:35.869+01:00 level=INFO msg="run: start" run_id=0019ea7f8b8dda4b7d7af4e8065e4 temp_dir=/var/folders/ly/.../T/xray-0019ea7f8b8dda4b7d7af4e8065e4-2676091357 workers=4
```

| Field | Meaning |
|---|---|
| `run_id` | Unique ULID for this run. Appears in `manifest.json`. |
| `temp_dir` | Scratch directory for clones. Removed on clean exit unless `--keep-clones`. |
| `workers` | Number of concurrent extract workers. Default 4. |

```
time=... level=INFO msg="run: cloning" repo=goreleaser/chglog
```

One line per repo, emitted as each clone begins. Clones run in parallel; this
line appears before the clone completes.

```
time=... level=INFO msg="run: extracting" repo=goreleaser/chglog connector=github
```

Emitted when an extract worker picks up a (repo, connector) job. If you see
many of these clustered together, the workers are pulling jobs as clones finish
(the expected behaviour since issue #159).

---

## Progress messages during extraction

```
time=... level=INFO msg="github: progress" repo=goreleaser/chglog stage=prs count=100 elapsed=14.624708ms
```

Emitted every 100 rows or every 30 seconds, whichever comes first. Used to
confirm the run is not stuck during multi-minute extractions.

| Field | Meaning |
|---|---|
| `stage` | Table being written. Values: `prs`, `commits`, `reviews`, `pr_comments`, `file_metrics`, `harness_artifacts`, `prs_sample`. |
| `count` | Rows written so far for this stage in this repo. |
| `elapsed` | Wall time since this stage started. |

```
time=... level=INFO msg="github: progress complete" repo=goreleaser/chglog stage=prs total=64 elapsed=590.393583ms
```

Emitted once when a stage finishes. `total` is the final row count for that
stage in that repo. A `prs_sample` stage here means the pre-bracket sparse
slice finished.

---

## Sparse-mode bucket messages

These messages are specific to sparse mode and only appear when `pr_inflection`
is configured.

### Bucket split — monthly exceeds 1 000-result cap

```
level=WARN msg="github: sparse: search bucket exceeds 1000-result cap; splitting to weekly" repo=acme-corp/api bucket=2022-01 total_count=1430
```

GitHub's search API caps results at 1 000 per query. When a monthly bucket
returns `total_count > 1000`, xray splits it into weekly sub-buckets (W1–W5)
and fetches each sub-bucket separately. The parent `2022-01` record is written
to provenance with `truncated=true`; each sub-bucket (`2022-01-W1`, etc.)
appears as its own provenance entry.

| Field | Meaning |
|---|---|
| `bucket` | Month label in `YYYY-MM` format. |
| `total_count` | GitHub's reported population size for that query. |

### Weekly sub-bucket still exceeds cap

```
level=WARN msg="github: sparse: weekly bucket also exceeds 1000-result cap; capping at search limit" repo=acme-corp/api bucket=2022-01-W1 total_count=1120
```

Weekly sub-buckets are the deepest split xray performs. If a weekly bucket
also exceeds 1 000, xray keeps the capped results (at most 1 000 from that
week) and marks the sub-bucket `truncated=true` in provenance. No further
recursion occurs.

### Template fetch error

```
level=WARN msg="github: sparse: fetch PR template" repo=acme-corp/api error=...
```

xray reads the repo's PR template to classify whether each PR matched a
template. If the fetch fails, template-match classification is skipped for all
PRs in the sparse slice. Row extraction continues normally.

### Bucket errors in provenance

When a bucket fetch fails, the error is recorded in
`extraction_provenance[*].errors` under the key `prs_sample:<YYYY-MM>`, and
`pagination_complete` is set to `false`. The log does not emit a separate line
for this — look at the manifest's provenance block to see which months were
affected.

---

## Sampling decisions

Whether a PR is included in the sample for a given month bucket depends on the
`pr_history_sample` strategy.

**Default strategy (`monthly:N`)** — xray fetches up to N PRs from GitHub's
relevance-ordered search results. GitHub determines relevance; xray takes the
first N results returned. If the month has fewer than N PRs, all are taken and
`actual == total` in provenance.

**Random strategy (`monthly:N:random`)** — xray fetches all PRs in the bucket
(up to the 1 000-result cap) and then selects N using a deterministic shuffle
seeded from `(repo_slug, bucket_month)`. The same seed produces the same sample
on every re-extraction, so quarterly re-runs produce comparable samples.

In both strategies, the provenance records `target` (N from config), `actual`
(rows written), and `total` (GitHub's reported population). When `actual ==
total`, the month had fewer PRs than the target; treat it as full fidelity for
that month.

---

## Rate-limit and backoff messages

These apply to all connectors, not only sparse mode.

### Transient retry

```
level=INFO msg="ratelimit: waiting before retry" attempt=1 wait=2s budget=transient budget_spent=2s
```

Emitted before each retry attempt. xray retries up to three times per request
with exponential backoff capped at 60 seconds cumulative.

| Field | Meaning |
|---|---|
| `attempt` | Which retry this is (1, 2, or 3). |
| `wait` | Duration xray will sleep before re-issuing the request. |
| `budget` | Retry budget category: `transient` (network errors, 5xx), `secondary_rate_limit` (GitHub anti-burst 403). |
| `budget_spent` | Total retry time spent so far on this request. |

### Primary rate-limit pacing

```
level=WARN msg="ratelimit: primary limit low, sleeping until reset" sleep=47s
```

Emitted when the `X-RateLimit-Remaining` header on a GitHub response falls
below the low-water mark (200 requests by default). xray proactively sleeps
until the reset window rather than hammering the API and receiving 429
responses. The run resumes automatically.

The corresponding progress event message (visible in `--output log` or `--output
json`) is:

```
primary limit low, waiting 47s
```

### Adaptive secondary-rate-limit pacing

```
adaptive pacing, waiting 3s
```

Emitted as a progress event (not in the structured log) when xray applies
adaptive back-off after hitting GitHub's secondary (anti-burst) rate limit. The
inter-request delay increases on each hit and decays gradually when requests
succeed. No operator action is needed; the run continues automatically.

---

## Pagination-complete vs pagination-truncated

`pagination_complete` in the manifest's `extraction_provenance` block reflects
whether xray walked all available pages for a connector.

- `"pagination_complete": true` — xray reached the natural end of the results
  for every stage in this repo.
- `"pagination_complete": false` — one of: context cancelled (Ctrl-C), budget
  exhausted after three retries, or a per-bucket error in sparse mode. The
  `errors` map in the same provenance block identifies the affected stage.

For sparse mode specifically, `pagination_complete: false` with an error key
like `prs_sample:2022-03` means the March 2022 bucket failed mid-fetch. PRs
from that month are absent or incomplete in the artifact; the analyser treats
this as **unknown**, not **no signal**.

---

## Final stats messages

```
time=... level=INFO msg="run: postprocess" incidents_linked=0 deploys_rolled_back=0 landed_via_pr_matched=0
```

Emitted after all connectors finish. These cross-cutting linkage counts update
foreign-key references across tables (e.g. linking incidents to deploys,
marking commits that landed via a PR). Zero values are normal for repos with
no matching signal across connectors.

```
time=... level=INFO msg="run: artifact" path=/tmp/xray-smoke-chglog/xray-fresh.tar.gz size=2348761 sha256=4a8d2e7f...
```

The final line of a successful run. `size` is in bytes. The `sha256` value is
what you send the consultant alongside the artifact for provenance verification.

Send the consultant: the `.tar.gz` artifact, the `.log` file, and the output of
`xray version`.

---

## Reading the manifest's sampling block

After the run, `manifest.json` contains a `sampling` field in each github
provenance entry where sparse mode was active:

```json
"sampling": {
  "inflection_date": "2023-06-01",
  "bracket_window": "12m",
  "bracket_start": "2022-06-01",
  "bracket_end": "2026-06-01",
  "strategy": "search_default_relevance",
  "buckets": [
    {"month": "2021-01", "target": 20, "actual": 20, "total": 47},
    {"month": "2021-02", "target": 20, "actual": 18, "total": 18},
    {"month": "2022-01", "target": 20, "actual": 20, "total": 1500, "truncated": true},
    {"month": "2022-01-W1", "target": 5, "actual": 5, "total": 420}
  ]
}
```

| Field | Meaning |
|---|---|
| `target` | N from `pr_history_sample`. |
| `actual` | Rows written for this bucket. |
| `total` | GitHub's reported population for the bucket's date range. |
| `truncated` | `true` when the bucket exceeded the 1 000-result cap and was split to weekly sub-buckets. |

When `actual < target`, the bucket had fewer PRs than requested — this is not
a failure. When `actual == total` and `total < target`, the month's full
population was extracted; treat it as full fidelity. When `truncated: true`,
the parent bucket record has no `actual` rows; look at the weekly sub-bucket
records that follow it.
