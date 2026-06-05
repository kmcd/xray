package model

// DDL applied at store-open time. Adding a table or a column with a default
// is non-breaking; removing or changing existing columns requires bumping
// SchemaVersion.
const DDL = `
CREATE TABLE IF NOT EXISTS _schema (
    schema_version INTEGER NOT NULL,
    tool_version TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS repos (
    slug TEXT PRIMARY KEY,
    default_branch TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    team TEXT NOT NULL,
    primary_language TEXT,
    created_at TEXT,
    is_fork INTEGER NOT NULL DEFAULT 0,
    is_archived INTEGER NOT NULL DEFAULT 0,
    visibility TEXT,
    contributor_count INTEGER,
    commits_in_window INTEGER,
    prs_in_window INTEGER,
    commits_all_time INTEGER,
    prs_all_time INTEGER
);

CREATE TABLE IF NOT EXISTS teams (
    name TEXT NOT NULL,
    repo TEXT NOT NULL,
    PRIMARY KEY (name, repo)
);

CREATE TABLE IF NOT EXISTS repo_languages (
    repo TEXT NOT NULL,
    language TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    PRIMARY KEY (repo, language)
);

CREATE TABLE IF NOT EXISTS branches (
    repo TEXT NOT NULL,
    name TEXT NOT NULL,
    last_commit_sha TEXT NOT NULL,
    last_commit_at TEXT NOT NULL,
    is_default INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo, name)
);

CREATE TABLE IF NOT EXISTS branch_protection (
    repo TEXT NOT NULL,
    branch TEXT NOT NULL,
    required_reviews INTEGER,
    required_checks TEXT,
    enforce_admins INTEGER,
    restricts_pushes INTEGER,
    PRIMARY KEY (repo, branch)
);

CREATE TABLE IF NOT EXISTS codeowners (
    repo TEXT NOT NULL,
    pattern TEXT NOT NULL,
    owner_handle TEXT NOT NULL,
    owner_type TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS commits (
    sha TEXT NOT NULL,
    repo TEXT NOT NULL,
    author_handle TEXT,
    committer_handle TEXT,
    authored_at TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    additions INTEGER NOT NULL DEFAULT 0,
    deletions INTEGER NOT NULL DEFAULT 0,
    files_changed INTEGER NOT NULL DEFAULT 0,
    message_subject TEXT,
    author_is_bot INTEGER NOT NULL DEFAULT 0,
    committer_is_bot INTEGER NOT NULL DEFAULT 0,
    signature_verified INTEGER,
    landed_via_pr INTEGER,
    reverts_sha TEXT,
    is_revert INTEGER NOT NULL DEFAULT 0,
    is_merge INTEGER NOT NULL DEFAULT 0,
    has_hotfix_marker INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (sha, repo)
);
CREATE INDEX IF NOT EXISTS idx_commits_repo_authored ON commits (repo, authored_at);

CREATE TABLE IF NOT EXISTS commit_files (
    commit_sha TEXT NOT NULL,
    repo TEXT NOT NULL,
    path TEXT NOT NULL,
    additions INTEGER NOT NULL DEFAULT 0,
    deletions INTEGER NOT NULL DEFAULT 0,
    change_type TEXT NOT NULL,
    prev_path TEXT
);
CREATE INDEX IF NOT EXISTS idx_commit_files_repo_path ON commit_files (repo, path);
CREATE INDEX IF NOT EXISTS idx_commit_files_sha ON commit_files (repo, commit_sha);

CREATE TABLE IF NOT EXISTS commit_coauthors (
    commit_sha TEXT NOT NULL,
    repo TEXT NOT NULL,
    handle TEXT NOT NULL,
    source TEXT NOT NULL,
    kind TEXT NOT NULL,
    PRIMARY KEY (commit_sha, repo, handle)
);

CREATE TABLE IF NOT EXISTS prs (
    number INTEGER NOT NULL,
    repo TEXT NOT NULL,
    title TEXT,
    opened_at TEXT NOT NULL,
    merged_at TEXT,
    closed_at TEXT,
    author_handle TEXT,
    additions INTEGER,
    deletions INTEGER,
    files_changed INTEGER,
    base_branch TEXT,
    head_sha TEXT,
    merge_sha TEXT,
    merge_method TEXT,
    is_draft INTEGER,
    ready_for_review_at TEXT,
    first_review_at TEXT,
    commit_count INTEGER,
    head_repo TEXT,
    force_pushed_after_review INTEGER,
    body_length INTEGER,
    template_match REAL,
    checklist_total INTEGER,
    checklist_checked INTEGER,
    has_risk_marker INTEGER,
    code_block_count INTEGER,
    image_count INTEGER,
    link_count INTEGER,
    issue_refs_count INTEGER,
    PRIMARY KEY (repo, number)
);

CREATE TABLE IF NOT EXISTS pr_commits (
    pr_number INTEGER NOT NULL,
    repo TEXT NOT NULL,
    sha TEXT NOT NULL,
    PRIMARY KEY (repo, pr_number, sha)
);

CREATE TABLE IF NOT EXISTS reviews (
    pr_number INTEGER NOT NULL,
    repo TEXT NOT NULL,
    reviewer_handle TEXT,
    submitted_at TEXT NOT NULL,
    state TEXT NOT NULL,
    body_length INTEGER
);
CREATE INDEX IF NOT EXISTS idx_reviews_pr ON reviews (repo, pr_number);

CREATE TABLE IF NOT EXISTS pr_comments (
    pr_number INTEGER NOT NULL,
    repo TEXT NOT NULL,
    author_handle TEXT,
    author_is_bot INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    body_length INTEGER,
    in_reply_to INTEGER,
    path TEXT
);
CREATE INDEX IF NOT EXISTS idx_pr_comments_pr ON pr_comments (repo, pr_number);

CREATE TABLE IF NOT EXISTS pr_review_requests (
    pr_number INTEGER NOT NULL,
    repo TEXT NOT NULL,
    requested_handle TEXT NOT NULL,
    requested_type TEXT NOT NULL,
    requested_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pr_labels (
    pr_number INTEGER NOT NULL,
    repo TEXT NOT NULL,
    label TEXT NOT NULL,
    PRIMARY KEY (repo, pr_number, label)
);

CREATE TABLE IF NOT EXISTS builds (
    id TEXT NOT NULL,
    repo TEXT NOT NULL,
    source TEXT NOT NULL,
    pipeline TEXT,
    status TEXT,
    conclusion TEXT,
    started_at TEXT,
    completed_at TEXT,
    duration_seconds INTEGER,
    commit_sha TEXT,
    branch TEXT,
    event TEXT,
    attempt INTEGER NOT NULL DEFAULT 1,
    rerun_of_id TEXT,
    created_at TEXT NOT NULL,
    pr_number INTEGER,
    PRIMARY KEY (repo, source, id)
);
CREATE INDEX IF NOT EXISTS idx_builds_repo_sha ON builds (repo, commit_sha);

CREATE TABLE IF NOT EXISTS build_jobs (
    build_id TEXT NOT NULL,
    repo TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT,
    conclusion TEXT,
    duration_seconds INTEGER,
    attempt INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_build_jobs_build ON build_jobs (repo, build_id);

CREATE TABLE IF NOT EXISTS deploys (
    id TEXT NOT NULL,
    repo TEXT NOT NULL,
    environment TEXT,
    deployed_at TEXT NOT NULL,
    commit_sha TEXT,
    source TEXT NOT NULL,
    status TEXT,
    supersedes_deploy_id TEXT,
    rolled_back INTEGER NOT NULL DEFAULT 0,
    trigger TEXT,
    release_tag TEXT,
    version TEXT,
    PRIMARY KEY (repo, source, id)
);
CREATE INDEX IF NOT EXISTS idx_deploys_repo_env ON deploys (repo, environment, deployed_at);

CREATE TABLE IF NOT EXISTS releases (
    repo TEXT NOT NULL,
    tag TEXT NOT NULL,
    name TEXT,
    created_at TEXT NOT NULL,
    sha TEXT,
    is_prerelease INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo, tag)
);

CREATE TABLE IF NOT EXISTS incidents (
    id TEXT NOT NULL,
    repo TEXT NOT NULL,
    source TEXT NOT NULL,
    opened_at TEXT NOT NULL,
    resolved_at TEXT,
    severity TEXT,
    occurrences INTEGER,
    release_ref TEXT,
    deploy_id TEXT,
    commit_sha TEXT,
    acknowledged_at TEXT,
    is_regression INTEGER NOT NULL DEFAULT 0,
    culprit_ref TEXT,
    PRIMARY KEY (repo, source, id)
);
CREATE INDEX IF NOT EXISTS idx_incidents_repo_opened ON incidents (repo, opened_at);

CREATE TABLE IF NOT EXISTS defects (
    id TEXT NOT NULL,
    repo TEXT NOT NULL,
    ticket_ref TEXT NOT NULL,
    source TEXT NOT NULL,
    opened_at TEXT NOT NULL,
    closed_at TEXT,
    PRIMARY KEY (repo, id)
);
CREATE INDEX IF NOT EXISTS idx_defects_repo_ticket ON defects (repo, ticket_ref);

CREATE TABLE IF NOT EXISTS file_metrics (
    repo TEXT NOT NULL,
    path TEXT NOT NULL,
    snapshot_sha TEXT NOT NULL,
    language TEXT,
    is_binary INTEGER NOT NULL DEFAULT 0,
    is_generated INTEGER NOT NULL DEFAULT 0,
    is_vendored INTEGER NOT NULL DEFAULT 0,
    is_test INTEGER NOT NULL DEFAULT 0,
    is_dependency_manifest INTEGER NOT NULL DEFAULT 0,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    loc_total INTEGER NOT NULL DEFAULT 0,
    loc_code INTEGER NOT NULL DEFAULT 0,
    loc_blank INTEGER NOT NULL DEFAULT 0,
    max_indent INTEGER NOT NULL DEFAULT 0,
    mean_indent REAL NOT NULL DEFAULT 0,
    max_line_length INTEGER NOT NULL DEFAULT 0,
    p95_line_length INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (repo, path)
);

CREATE TABLE IF NOT EXISTS harness_artifacts (
    repo TEXT NOT NULL,
    path TEXT NOT NULL,
    tool TEXT NOT NULL,
    kind TEXT NOT NULL,
    line_count INTEGER NOT NULL DEFAULT 0,
    first_seen_commit TEXT,
    first_seen_at TEXT,
    last_modified_at TEXT,
    content TEXT,
    PRIMARY KEY (repo, path)
);
`
