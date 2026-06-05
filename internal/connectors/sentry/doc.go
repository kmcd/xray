// Package sentry implements the Sentry API connector. It populates
// `incidents` for repos configured in xray.toml whose Sentry project slug
// is mapped to a repo slug under [connectors.sentry.projects].
//
// Endpoints used:
//
//   - GET /organizations/{org}/                            (Ping)
//   - GET /projects/{org}/{project-slug}/issues/?...       (Extract)
//
// All requests authenticate via the `Authorization: Bearer <token>` header.
//
// Simplifications versus the spec:
//
//   - ResolvedAt is set to issue.lastSeen only when issue.status == "resolved".
//     Sentry does not consistently expose a dedicated resolved-at timestamp on
//     the issues list payload; lastSeen is the closest neutral signal. When
//     the issue is not resolved the field is left nil.
//   - ReleaseRef is taken from issue.firstRelease.shortVersion (falling back
//     to firstRelease.version) as returned by the issues list endpoint. The
//     connector does not fan out to GET /issues/{id}/ to expand release
//     details; this avoids a per-issue request multiplier on large
//     extractions at the cost of leaving ReleaseRef empty when firstRelease
//     is absent from the list response.
//   - AcknowledgedAt is always nil. Sentry has no native acknowledge concept
//     distinct from status transitions; the canonical column is reserved for
//     trackers (e.g. PagerDuty integrations) that surface it.
//   - DeployID and CommitSHA are left empty. They are resolved later from
//     ReleaseRef by the cross-cutting M10 pass that joins release identifiers
//     across connectors.
//   - IsRegression is a heuristic: true when issue.isUnhandled is true or
//     when the lowercased issue message / culprit contains the substring
//     "regression". Sentry's own regression flag is exposed on issue detail
//     payloads, not on every list shape, so this remains a best-effort
//     signal rather than a faithful mirror.
//   - CulpritRef is the raw issue.culprit string. Sentry attributes this
//     itself from event telemetry; the connector does not synthesise one
//     when absent.
//
// Window filter: an issue is emitted when its firstSeen falls inside the
// configured extraction window. Issues whose firstSeen predates the window
// are not emitted even if they had activity inside the window; this keeps
// the connector's behaviour predictable across re-runs.
package sentry
