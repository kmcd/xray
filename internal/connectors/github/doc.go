// Package github is the xray GitHub connector. It populates the bulk of
// the canonical model: repo metadata, branches, branch protection (where
// accessible), codeowners, commits, commit_files, commit_coauthors, prs,
// pr_commits, reviews, pr_comments, pr_review_requests, pr_labels,
// releases, repo_languages, and deploys derived from releases.
//
// All HTTP traffic flows through an oauth2 client wrapped with the shared
// ratelimit transport. The connector is strictly read-only and never
// issues PATCH/POST/DELETE requests. No source-content, PR or commit body
// text is persisted: bodies are parsed for structured signals at extract
// time and discarded.
//
// File-metric and harness-artifact extraction live in file_metrics.go and
// harness.go (owned by the M4 agent in the same package). Extract calls
// those helpers via in-package forward references.
package github
