// Package bugsnag implements the Bugsnag Data Access API connector. It
// populates `incidents` for repos whose Bugsnag project ID is mapped via
// `[connectors.bugsnag.projects]` in xray.toml.
//
// Notes versus the canonical schema:
//
//   - CulpritRef is emitted as the empty string. Bugsnag's top stack frame
//     is not an exact equivalent of Sentry's culprit and is intentionally
//     left blank rather than synthesised (spec rule: emit null where the
//     source's native shape does not cleanly map).
//   - AcknowledgedAt is left nil; Bugsnag has no native acknowledge concept.
//   - CommitSHA is populated from `release.revision` when present; the field
//     carries the git SHA of the deployed build and is included on every
//     Bugsnag error payload that has an associated release.
//   - DeployID is left blank; the Bugsnag Data Access API has no
//     deploy-tracking endpoint.
//   - Window filtering is by Bugsnag's `first_seen` field: only errors whose
//     `first_seen` falls inside the configured window are emitted.
package bugsnag
