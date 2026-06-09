## Status

Accepted. v0.2.x. Bumps schema_version 1→2.

## Context

Every `*_handle` column naming an individual identity stored the raw git
ident or GitHub login. The spec requires team-level measurement with no
individual-developer identifiers in any output table. Raw handles are
individual identifiers; storing them violates the privacy contract even
though they are used only for linkage.

## Decision

Every `*_handle` column that names an individual identity
(`commits.author_handle`, `commits.committer_handle`,
`commit_coauthors.handle`, `prs.author_handle`, `reviews.reviewer_handle`,
`pr_comments.author_handle`) emits an opaque `h_<15 digits>` token instead of
the raw login or git ident. The pre-image is the canonical `Name <email>`
after `.mailmap` resolution (for commit-side identities) or the lowercased
`@login` (for GitHub-side identities). Hash = sha256(canonical), low 64
bits, modulo 10^15, zero-padded.

`pr_review_requests.requested_handle` and `codeowners.owner_handle` stay raw
because these tables carry user *or* team identifiers; the team-vs-user
distinction is load-bearing for downstream analysis, and the team slug is not
PII.

This is a `schema_version` bump (1→2) because changing the *semantics* of an
existing column from "raw git author name" to "opaque hash" is a breaking
change per the schema versioning rules, even though column type and name are
unchanged. An analyser pinned to schema_version=1 would see plausible-looking
strings, decode them as raw logins, and silently produce wrong verdicts.

**Why hash to digits instead of hex.** The assay v1.1 boundary check is
`^h_\d{3,}$` (digits only). Hex hashes would fail the check.
`fmt.Sprintf("h_%015d", uint64(sha256[:8]) % 1e15)` — 10^15 distinct values,
birthday-collision parity near ~10^7.5 distinct authors, well above any
plausible team-of-teams scale.

**Why the `@`-prefix on login canonicalisation.** GitHub logins and commit
`Name <email>` strings live in disjoint namespaces. Without per-user email
resolution there's no reliable way to link a commit author to their GitHub
login. Hashing `"alice"` (login) and `"alice"` (commit name, no email) to
the same value would silently fuse unrelated people. The `@` prefix on
`canonicalLogin` keeps the two surfaces separate.

## Consequences

**Positive.** Handle columns satisfy the no-individual-identifier contract.
Analysers that see schema_version=1 error out rather than silently degrading.

**Negative.** The linkage gap between commit-side and GitHub-side handles is
documented but not resolved until per-user email resolution lands.
`pr_review_requests` and `codeowners` carry raw handles, which is a partial
exception to the rule.

**Neutral.** `manifest.mailmap_applied` aggregates whether `.mailmap`
resolution was applied per repo.

## How to apply

`internal/connectors/github/handle.go` holds the helpers; mailmap resolution
lives in `internal/gitcli/mailmap.go`. Insert sites in `commits.go`,
`coauthors.go`, `prs.go`, `reviews.go`, `pr_comments.go`.
`manifest.mailmap_applied` is aggregated in
`internal/run/run.go::aggregateMailmapApplied`. Schema-version assertion in
`internal/model/schema_test.go::TestDDL_SchemaVersionConstant` updated to
expect `2`.
