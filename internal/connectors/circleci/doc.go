// Package circleci implements the CircleCI v2 API connector. It populates
// `builds` and `build_jobs` for repos configured in xray.toml whose
// CircleCI project slug ("gh/<owner>/<name>") is accessible to the
// configured token.
//
// Simplifications versus the spec:
//
//   - CircleCI does not classify pipeline triggers as cleanly as GitHub
//     Actions does. Every emitted Build sets Event="push".
//   - CircleCI's rerun model is more granular than the canonical
//     (Attempt, RerunOfID) pair; the connector emits Attempt=1 and
//     RerunOfID="" rather than synthesise misleading lineage.
//   - PRNumber is left nil; recovering it requires correlating webhook
//     payloads not exposed by the public v2 endpoints used here.
//   - Job DurationSeconds is computed from started_at/stopped_at on the
//     workflow job summary when both are present; otherwise nil. The
//     connector deliberately does not fan out to per-job detail to avoid
//     a request-per-job multiplier.
package circleci
