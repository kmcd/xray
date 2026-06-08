package store

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	// Pure-Go SQLite driver; registers itself under the "sqlite" name.
	_ "modernc.org/sqlite"

	"github.com/kmcd/xray/internal/model"
)

// Store wraps a SQLite database and implements connector.Sink with
// prepared insert statements. Concurrent connector workers share one
// Store; per-call inserts are serialised behind a mutex so the single
// connection writer is safe.
type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	stmt statements
}

type statements struct {
	repo              *sql.Stmt
	teamRepo          *sql.Stmt
	repoLanguage      *sql.Stmt
	branch            *sql.Stmt
	branchProtection  *sql.Stmt
	codeowner         *sql.Stmt
	commit            *sql.Stmt
	commitFile        *sql.Stmt
	commitCoauthor    *sql.Stmt
	pr                *sql.Stmt
	prCommit          *sql.Stmt
	review            *sql.Stmt
	prComment         *sql.Stmt
	prReviewRequest   *sql.Stmt
	prLabel           *sql.Stmt
	build             *sql.Stmt
	buildJob          *sql.Stmt
	deploy            *sql.Stmt
	release           *sql.Stmt
	incident          *sql.Stmt
	defect            *sql.Stmt
	fileMetric        *sql.Stmt
	harnessArtifact   *sql.Stmt
}

// Open opens (or creates) a SQLite database at path, applies the canonical
// DDL, records a _schema row capturing the schema and tool versions, and
// prepares one insert statement per Sink method.
func Open(path string, toolVersion string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// modernc/sqlite has its own single-writer semantics; keep things
	// serial to avoid SQLITE_BUSY under the worker pool.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(model.DDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: apply DDL: %w", err)
	}

	if _, err := db.Exec(
		`INSERT INTO _schema (schema_version, tool_version, applied_at) VALUES (?, ?, ?)`,
		model.SchemaVersion, toolVersion, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: record schema row: %w", err)
	}

	s := &Store{db: db}
	if err := s.prepare(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: prepare statements: %w", err)
	}
	return s, nil
}

// DB exposes the underlying *sql.DB for cross-cutting passes (postprocess
// linkage) that need to issue UPDATEs against multiple tables. The store's
// mutex does not guard direct DB access; callers must coordinate with
// other writers, which in practice means using DB only after all
// connector extraction has finished.
func (s *Store) DB() *sql.DB {
	return s.db
}

// SquashStats returns counts of merged PRs and squash-merged PRs across the
// entire artifact. Used by run.go after extraction to populate the
// manifest's `n_total_merged_prs` / `n_squash_merged_prs` / `squash_rate`
// fields; assay treats squash_rate > 0.5 as the Tornhill Ch 9 caveat
// threshold for coupling-derived metrics.
//
// merge_method classification is per ADR 021 (parent count + PR-head
// reachability), already computed at extract time and stored in the
// prs.merge_method column — no re-derivation here.
func (s *Store) SquashStats() (nSquash, nMerged int, err error) {
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prs WHERE merged_at IS NOT NULL`).Scan(&nMerged); err != nil {
		return 0, 0, fmt.Errorf("store: count merged prs: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prs WHERE merged_at IS NOT NULL AND merge_method = 'squash'`).Scan(&nSquash); err != nil {
		return 0, 0, fmt.Errorf("store: count squash prs: %w", err)
	}
	return nSquash, nMerged, nil
}

// Close releases prepared statements and the underlying database handle.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, stmt := range []*sql.Stmt{
		s.stmt.repo, s.stmt.teamRepo, s.stmt.repoLanguage, s.stmt.branch,
		s.stmt.branchProtection, s.stmt.codeowner, s.stmt.commit, s.stmt.commitFile,
		s.stmt.commitCoauthor, s.stmt.pr, s.stmt.prCommit, s.stmt.review,
		s.stmt.prComment, s.stmt.prReviewRequest, s.stmt.prLabel, s.stmt.build,
		s.stmt.buildJob, s.stmt.deploy, s.stmt.release, s.stmt.incident,
		s.stmt.defect, s.stmt.fileMetric, s.stmt.harnessArtifact,
	} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
	return s.db.Close()
}

func (s *Store) prepare() error {
	type p struct {
		dst **sql.Stmt
		sql string
	}
	for _, q := range []p{
		{&s.stmt.repo, `INSERT OR REPLACE INTO repos (slug, default_branch, head_sha, team, primary_language, created_at, is_fork, is_archived, visibility, contributor_count, commits_in_window, prs_in_window, commits_all_time, prs_all_time) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.teamRepo, `INSERT OR IGNORE INTO teams (name, repo) VALUES (?,?)`},
		{&s.stmt.repoLanguage, `INSERT OR REPLACE INTO repo_languages (repo, language, bytes) VALUES (?,?,?)`},
		{&s.stmt.branch, `INSERT OR REPLACE INTO branches (repo, name, last_commit_sha, last_commit_at, is_default) VALUES (?,?,?,?,?)`},
		{&s.stmt.branchProtection, `INSERT OR REPLACE INTO branch_protection (repo, branch, required_reviews, required_checks, enforce_admins, restricts_pushes) VALUES (?,?,?,?,?,?)`},
		{&s.stmt.codeowner, `INSERT INTO codeowners (repo, pattern, owner_handle, owner_type) VALUES (?,?,?,?)`},
		{&s.stmt.commit, `INSERT OR REPLACE INTO commits (sha, repo, author_handle, committer_handle, authored_at, committed_at, additions, deletions, files_changed, message_subject, author_is_bot, committer_is_bot, signature_verified, landed_via_pr, reverts_sha, is_revert, is_merge, has_hotfix_marker) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.commitFile, `INSERT INTO commit_files (commit_sha, repo, path, additions, deletions, change_type, prev_path) VALUES (?,?,?,?,?,?,?)`},
		{&s.stmt.commitCoauthor, `INSERT OR REPLACE INTO commit_coauthors (commit_sha, repo, handle, source, kind) VALUES (?,?,?,?,?)`},
		{&s.stmt.pr, `INSERT OR REPLACE INTO prs (number, repo, title, opened_at, merged_at, closed_at, author_handle, additions, deletions, files_changed, base_branch, head_sha, merge_sha, merge_method, is_draft, ready_for_review_at, first_review_at, commit_count, head_repo, force_pushed_after_review, body_length, template_match, checklist_total, checklist_checked, has_risk_marker, code_block_count, image_count, link_count, issue_refs_count) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.prCommit, `INSERT OR IGNORE INTO pr_commits (pr_number, repo, sha) VALUES (?,?,?)`},
		{&s.stmt.review, `INSERT INTO reviews (pr_number, repo, reviewer_handle, submitted_at, state, body_length) VALUES (?,?,?,?,?,?)`},
		{&s.stmt.prComment, `INSERT INTO pr_comments (pr_number, repo, author_handle, author_is_bot, created_at, kind, body_length, in_reply_to, path) VALUES (?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.prReviewRequest, `INSERT INTO pr_review_requests (pr_number, repo, requested_handle, requested_type, requested_at) VALUES (?,?,?,?,?)`},
		{&s.stmt.prLabel, `INSERT OR IGNORE INTO pr_labels (pr_number, repo, label) VALUES (?,?,?)`},
		{&s.stmt.build, `INSERT OR REPLACE INTO builds (id, repo, source, pipeline, status, conclusion, started_at, completed_at, duration_seconds, commit_sha, branch, event, attempt, rerun_of_id, created_at, pr_number) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.buildJob, `INSERT INTO build_jobs (build_id, repo, name, status, conclusion, duration_seconds, attempt) VALUES (?,?,?,?,?,?,?)`},
		{&s.stmt.deploy, `INSERT OR REPLACE INTO deploys (id, repo, environment, deployed_at, commit_sha, source, status, supersedes_deploy_id, rolled_back, trigger, release_tag, version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.release, `INSERT OR REPLACE INTO releases (repo, tag, name, created_at, sha, is_prerelease) VALUES (?,?,?,?,?,?)`},
		{&s.stmt.incident, `INSERT OR REPLACE INTO incidents (id, repo, source, opened_at, resolved_at, severity, occurrences, release_ref, deploy_id, commit_sha, acknowledged_at, is_regression, culprit_ref) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.defect, `INSERT OR REPLACE INTO defects (id, repo, ticket_ref, source, opened_at, closed_at) VALUES (?,?,?,?,?,?)`},
		{&s.stmt.fileMetric, `INSERT OR REPLACE INTO file_metrics (repo, path, snapshot_sha, language, is_binary, is_generated, is_vendored, is_test, is_dependency_manifest, size_bytes, loc_total, loc_code, loc_blank, max_indent, mean_indent, max_line_length, p95_line_length) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{&s.stmt.harnessArtifact, `INSERT OR REPLACE INTO harness_artifacts (repo, path, tool, kind, line_count, first_seen_commit, first_seen_at, last_modified_at, content) VALUES (?,?,?,?,?,?,?,?,?)`},
	} {
		stmt, err := s.db.Prepare(q.sql)
		if err != nil {
			return fmt.Errorf("prepare %q: %w", q.sql, err)
		}
		*q.dst = stmt
	}
	return nil
}

// --- helpers ---

func rfc(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func nrfc(t *time.Time) any {
	if t == nil {
		return nil
	}
	return rfc(*t)
}

func nint(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nbool(p *bool) any {
	if p == nil {
		return nil
	}
	return b2i(*p)
}

func nfloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nint64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nstr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- Sink implementation ---

func (s *Store) InsertRepo(r model.Repo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.repo.Exec(
		r.Slug, r.DefaultBranch, r.HeadSHA, r.Team,
		nstr(r.PrimaryLanguage), nrfc(r.CreatedAt),
		b2i(r.IsFork), b2i(r.IsArchived), nstr(r.Visibility),
		r.ContributorCount, r.CommitsInWindow, r.PRsInWindow,
		r.CommitsAllTime, r.PRsAllTime,
	)
	return err
}

func (s *Store) InsertTeamRepo(team, repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.teamRepo.Exec(team, repo)
	return err
}

func (s *Store) InsertRepoLanguage(r model.RepoLanguage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.repoLanguage.Exec(r.Repo, r.Language, r.Bytes)
	return err
}

func (s *Store) InsertBranch(b model.Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.branch.Exec(b.Repo, b.Name, b.LastCommitSHA, rfc(b.LastCommitAt), b2i(b.IsDefault))
	return err
}

func (s *Store) InsertBranchProtection(b model.BranchProtection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.branchProtection.Exec(
		b.Repo, b.Branch, nint(b.RequiredReviews), nstr(b.RequiredChecks),
		b2i(b.EnforceAdmins), b2i(b.RestrictsPushes),
	)
	return err
}

func (s *Store) InsertCodeowner(c model.Codeowner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.codeowner.Exec(c.Repo, c.Pattern, c.OwnerHandle, c.OwnerType)
	return err
}

func (s *Store) InsertCommit(c model.Commit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.commit.Exec(
		c.SHA, c.Repo, nstr(c.AuthorHandle), nstr(c.CommitterHandle),
		rfc(c.AuthoredAt), rfc(c.CommittedAt),
		c.Additions, c.Deletions, c.FilesChanged, nstr(c.MessageSubject),
		b2i(c.AuthorIsBot), b2i(c.CommitterIsBot),
		nbool(c.SignatureVerified), nbool(c.LandedViaPR),
		nstr(c.RevertsSHA), b2i(c.IsRevert), b2i(c.IsMerge), b2i(c.HasHotfixMarker),
	)
	return err
}

func (s *Store) InsertCommitFile(c model.CommitFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.commitFile.Exec(
		c.CommitSHA, c.Repo, c.Path, c.Additions, c.Deletions,
		c.ChangeType, nstr(c.PrevPath),
	)
	return err
}

func (s *Store) InsertCommitCoauthor(c model.CommitCoauthor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.commitCoauthor.Exec(c.CommitSHA, c.Repo, c.Handle, c.Source, c.Kind)
	return err
}

func (s *Store) InsertPR(p model.PR) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.pr.Exec(
		p.Number, p.Repo, nstr(p.Title), rfc(p.OpenedAt),
		nrfc(p.MergedAt), nrfc(p.ClosedAt), nstr(p.AuthorHandle),
		p.Additions, p.Deletions, p.FilesChanged,
		nstr(p.BaseBranch), nstr(p.HeadSHA), nstr(p.MergeSHA), nstr(p.MergeMethod),
		b2i(p.IsDraft), nrfc(p.ReadyForReviewAt), nrfc(p.FirstReviewAt),
		p.CommitCount, nstr(p.HeadRepo), b2i(p.ForcePushedAfterReview),
		p.BodyLength, nfloat(p.TemplateMatch),
		p.ChecklistTotal, p.ChecklistChecked, b2i(p.HasRiskMarker),
		p.CodeBlockCount, p.ImageCount, p.LinkCount, p.IssueRefsCount,
	)
	return err
}

func (s *Store) InsertPRCommit(p model.PRCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.prCommit.Exec(p.PRNumber, p.Repo, p.SHA)
	return err
}

func (s *Store) InsertReview(r model.Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.review.Exec(r.PRNumber, r.Repo, nstr(r.ReviewerHandle), rfc(r.SubmittedAt), r.State, r.BodyLength)
	return err
}

func (s *Store) InsertPRComment(c model.PRComment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.prComment.Exec(
		c.PRNumber, c.Repo, nstr(c.AuthorHandle), b2i(c.AuthorIsBot),
		rfc(c.CreatedAt), c.Kind, c.BodyLength, nint64(c.InReplyTo), nstr(c.Path),
	)
	return err
}

func (s *Store) InsertPRReviewRequest(r model.PRReviewRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.prReviewRequest.Exec(r.PRNumber, r.Repo, r.RequestedHandle, r.RequestedType, rfc(r.RequestedAt))
	return err
}

func (s *Store) InsertPRLabel(l model.PRLabel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.prLabel.Exec(l.PRNumber, l.Repo, l.Label)
	return err
}

func (s *Store) InsertBuild(b model.Build) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.build.Exec(
		b.ID, b.Repo, b.Source, nstr(b.Pipeline), nstr(b.Status), nstr(b.Conclusion),
		nrfc(b.StartedAt), nrfc(b.CompletedAt), nint(b.DurationSeconds),
		nstr(b.CommitSHA), nstr(b.Branch), nstr(b.Event),
		b.Attempt, nstr(b.RerunOfID), rfc(b.CreatedAt), nint(b.PRNumber),
	)
	return err
}

func (s *Store) InsertBuildJob(j model.BuildJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.buildJob.Exec(
		j.BuildID, j.Repo, j.Name, nstr(j.Status), nstr(j.Conclusion),
		nint(j.DurationSeconds), j.Attempt,
	)
	return err
}

func (s *Store) InsertDeploy(d model.Deploy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.deploy.Exec(
		d.ID, d.Repo, nstr(d.Environment), rfc(d.DeployedAt), nstr(d.CommitSHA),
		d.Source, nstr(d.Status), nstr(d.SupersedesDeployID), b2i(d.RolledBack),
		nstr(d.Trigger), nstr(d.ReleaseTag), nstr(d.Version),
	)
	return err
}

func (s *Store) InsertRelease(r model.Release) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.release.Exec(r.Repo, r.Tag, nstr(r.Name), rfc(r.CreatedAt), nstr(r.SHA), b2i(r.IsPrerelease))
	return err
}

func (s *Store) InsertIncident(i model.Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.incident.Exec(
		i.ID, i.Repo, i.Source, rfc(i.OpenedAt), nrfc(i.ResolvedAt),
		nstr(i.Severity), i.Occurrences, nstr(i.ReleaseRef),
		nstr(i.DeployID), nstr(i.CommitSHA), nrfc(i.AcknowledgedAt),
		b2i(i.IsRegression), nstr(i.CulpritRef),
	)
	return err
}

func (s *Store) InsertDefect(d model.Defect) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.defect.Exec(d.ID, d.Repo, d.TicketRef, d.Source, rfc(d.OpenedAt), nrfc(d.ClosedAt))
	return err
}

func (s *Store) InsertFileMetric(f model.FileMetric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.fileMetric.Exec(
		f.Repo, f.Path, f.SnapshotSHA, nstr(f.Language),
		b2i(f.IsBinary), b2i(f.IsGenerated), b2i(f.IsVendored),
		b2i(f.IsTest), b2i(f.IsDependencyManifest),
		f.SizeBytes, f.LOCTotal, f.LOCCode, f.LOCBlank,
		f.MaxIndent, f.MeanIndent, f.MaxLineLength, f.P95LineLength,
	)
	return err
}

func (s *Store) InsertHarnessArtifact(h model.HarnessArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stmt.harnessArtifact.Exec(
		h.Repo, h.Path, h.Tool, h.Kind, h.LineCount,
		nstr(h.FirstSeenCommit), rfc(h.FirstSeenAt), rfc(h.LastModifiedAt),
		nstr(h.Content),
	)
	return err
}
