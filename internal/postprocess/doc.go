// Package postprocess runs cross-cutting linkage passes against the
// populated metrics SQLite store after every connector has finished
// extracting and before the manifest is written.
//
// Two passes are implemented in v1:
//
//   - Incident linkage: for each incident with a release_ref but no
//     deploy_id, find a matching deploys row (release_tag or version)
//     and populate deploy_id + commit_sha. Falls back to releases.tag
//     for commit_sha alone when no deploy matches.
//
//   - Deploy rollback linkage: within each (repo, environment) group,
//     identify deploys that re-ship an older commit while skipping the
//     immediately prior commit. Such deploys are marked as superseding
//     the prior deploy, and the prior deploy is marked rolled_back.
package postprocess
