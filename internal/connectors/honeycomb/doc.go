// Package honeycomb implements the Honeycomb API connector. It populates
// `deploys` from deploy markers on a configured dataset, and best-effort
// augments `incidents` from SLO burn alerts.
//
// Simplifications versus the spec:
//
//   - Honeycomb exposes no per-repo concept. To attribute deploys to a
//     repo, the connector picks the first repo (alphabetical by slug)
//     handed to it across Extract calls and tags every emitted Deploy /
//     Incident to that one repo. Other repos' Extract calls return an
//     empty Provenance recording the skip in the manifest. This is a v1
//     limitation; per-marker repo attribution would need a Honeycomb
//     convention (e.g. a "repo" marker tag) the spec has not pinned.
//   - Deploy markers do not carry a commit SHA natively, so CommitSHA is
//     emitted as "". The marker `type` field is used for Environment and
//     `message` for Version where present.
//   - SLO burn-alert ingestion is best-effort: any error in /slos or
//     /burn_alerts is logged at warn level and skipped, leaving an empty
//     `incidents` contribution rather than failing the run.
package honeycomb
