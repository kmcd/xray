package model_test

import (
	"database/sql"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/kmcd/xray/internal/model"
)

// mustExec runs a statement and fails the test on error, including the query
// text in the failure message so column-mismatch failures are easy to localise.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec: %v\nquery: %s", err, query)
	}
}

// openMemDB opens an in-memory SQLite database with the canonical DDL applied.
func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(model.DDL); err != nil {
		t.Fatalf("apply DDL: %v", err)
	}
	return db
}

// rfc returns t formatted as RFC3339 UTC, matching the store's convention.
func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// mustParseTime parses an RFC3339 string and fails the test on error.
func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// fixtureTime is a single sentinel timestamp used across roundtrip subtests.
func fixtureTime() time.Time {
	return time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
}

func TestDDL_SchemaVersionConstant(t *testing.T) {
	if model.SchemaVersion != 2 {
		t.Fatalf("SchemaVersion = %d, want 2 (bump this assertion deliberately when changing the schema)", model.SchemaVersion)
	}
}

func TestDDL_ApplyClean(t *testing.T) {
	db := openMemDB(t)

	wantTables := []string{
		"_schema", "repos", "teams", "repo_languages",
		"branches", "branch_protection", "codeowners",
		"commits", "commit_files", "commit_coauthors",
		"prs", "pr_commits", "reviews", "pr_comments",
		"pr_review_requests", "pr_labels",
		"builds", "build_jobs", "deploys", "releases",
		"incidents", "defects", "file_metrics", "harness_artifacts",
		"file_complexity_history",
	}

	got := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[n] = true
	}
	_ = rows.Close()

	for _, name := range wantTables {
		if !got[name] {
			t.Errorf("missing table %q", name)
		}
	}

	// Indexes declared in the DDL.
	wantIndexes := []string{
		"idx_commits_repo_authored",
		"idx_commit_files_repo_path",
		"idx_commit_files_sha",
		"idx_reviews_pr",
		"idx_pr_comments_pr",
		"idx_builds_repo_sha",
		"idx_deploys_repo_env",
		"idx_incidents_repo_opened",
		"idx_defects_repo_ticket",
		"idx_fch_repo_path",
	}
	gotIdx := map[string]bool{}
	irows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	for irows.Next() {
		var n string
		if err := irows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotIdx[n] = true
	}
	_ = irows.Close()
	for _, name := range wantIndexes {
		if !gotIdx[name] {
			t.Errorf("missing index %q", name)
		}
	}
}

func TestDDL_RowRoundTrip(t *testing.T) {
	now := fixtureTime()
	nowStr := rfc(now)
	later := now.Add(time.Hour)
	laterStr := rfc(later)

	t.Run("repos", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO repos (slug, default_branch, head_sha, team, primary_language, created_at, is_fork, is_archived, visibility, contributor_count, commits_in_window, prs_in_window, commits_all_time, prs_all_time) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"kmcd/repo-1", "main", "sha-head", "team-a", "Go", nowStr, 1, 0, "public", 7, 11, 13, 17, 19,
		)
		var got model.Repo
		var createdAt string
		var isFork, isArchived int
		if err := db.QueryRow(`SELECT slug, default_branch, head_sha, team, primary_language, created_at, is_fork, is_archived, visibility, contributor_count, commits_in_window, prs_in_window, commits_all_time, prs_all_time FROM repos`).Scan(
			&got.Slug, &got.DefaultBranch, &got.HeadSHA, &got.Team, &got.PrimaryLanguage, &createdAt,
			&isFork, &isArchived, &got.Visibility, &got.ContributorCount, &got.CommitsInWindow,
			&got.PRsInWindow, &got.CommitsAllTime, &got.PRsAllTime,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.IsFork = isFork == 1
		got.IsArchived = isArchived == 1
		ct := mustParseTime(t, createdAt)
		got.CreatedAt = &ct
		want := model.Repo{
			Slug: "kmcd/repo-1", DefaultBranch: "main", HeadSHA: "sha-head",
			Team: "team-a", PrimaryLanguage: "Go", CreatedAt: &ct,
			IsFork: true, IsArchived: false, Visibility: "public",
			ContributorCount: 7, CommitsInWindow: 11, PRsInWindow: 13,
			CommitsAllTime: 17, PRsAllTime: 19,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Repo mismatch\n got: %+v\nwant: %+v", got, want)
		}
	})

	t.Run("teams", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO teams (name, repo) VALUES (?, ?)`, "team-a", "kmcd/repo-1")
		var name, repo string
		if err := db.QueryRow(`SELECT name, repo FROM teams`).Scan(&name, &repo); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name != "team-a" || repo != "kmcd/repo-1" {
			t.Errorf("got (%q,%q), want (team-a, kmcd/repo-1)", name, repo)
		}
	})

	t.Run("repo_languages", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO repo_languages (repo, language, bytes) VALUES (?,?,?)`,
			"kmcd/repo-1", "Go", int64(12345))
		var got model.RepoLanguage
		if err := db.QueryRow(`SELECT repo, language, bytes FROM repo_languages`).Scan(&got.Repo, &got.Language, &got.Bytes); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := model.RepoLanguage{Repo: "kmcd/repo-1", Language: "Go", Bytes: 12345}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("branches", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO branches (repo, name, last_commit_sha, last_commit_at, is_default) VALUES (?,?,?,?,?)`,
			"kmcd/repo-1", "main", "sha-b", nowStr, 1)
		var got model.Branch
		var ts string
		var isDef int
		if err := db.QueryRow(`SELECT repo, name, last_commit_sha, last_commit_at, is_default FROM branches`).Scan(
			&got.Repo, &got.Name, &got.LastCommitSHA, &ts, &isDef,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.LastCommitAt = mustParseTime(t, ts)
		got.IsDefault = isDef == 1
		want := model.Branch{Repo: "kmcd/repo-1", Name: "main", LastCommitSHA: "sha-b", LastCommitAt: got.LastCommitAt, IsDefault: true}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("branch_protection", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO branch_protection (repo, branch, required_reviews, required_checks, enforce_admins, restricts_pushes) VALUES (?,?,?,?,?,?)`,
			"kmcd/repo-1", "main", 2, "ci/test,ci/lint", 1, 0)
		var got model.BranchProtection
		var rr int
		var ea, rp int
		if err := db.QueryRow(`SELECT repo, branch, required_reviews, required_checks, enforce_admins, restricts_pushes FROM branch_protection`).Scan(
			&got.Repo, &got.Branch, &rr, &got.RequiredChecks, &ea, &rp,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.RequiredReviews = &rr
		got.EnforceAdmins = ea == 1
		got.RestrictsPushes = rp == 1
		want := model.BranchProtection{Repo: "kmcd/repo-1", Branch: "main", RequiredReviews: &rr, RequiredChecks: "ci/test,ci/lint", EnforceAdmins: true, RestrictsPushes: false}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("codeowners", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO codeowners (repo, pattern, owner_handle, owner_type) VALUES (?,?,?,?)`,
			"kmcd/repo-1", "*.go", "alice", "user")
		var got model.Codeowner
		if err := db.QueryRow(`SELECT repo, pattern, owner_handle, owner_type FROM codeowners`).Scan(
			&got.Repo, &got.Pattern, &got.OwnerHandle, &got.OwnerType,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := model.Codeowner{Repo: "kmcd/repo-1", Pattern: "*.go", OwnerHandle: "alice", OwnerType: "user"}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("commits", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO commits (sha, repo, author_handle, committer_handle, authored_at, committed_at, additions, deletions, files_changed, message_subject, author_is_bot, committer_is_bot, signature_verified, landed_via_pr, reverts_sha, is_revert, is_merge, has_hotfix_marker) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"sha-c", "kmcd/repo-1", "alice", "bob", nowStr, laterStr,
			10, 5, 3, "feat: add x", 0, 0, 1, 1, "sha-prev", 0, 0, 1,
		)
		var got model.Commit
		var aAt, cAt string
		var aBot, cBot, isRev, isMerge, hotfix int
		var sigVer, landed int
		if err := db.QueryRow(`SELECT sha, repo, author_handle, committer_handle, authored_at, committed_at, additions, deletions, files_changed, message_subject, author_is_bot, committer_is_bot, signature_verified, landed_via_pr, reverts_sha, is_revert, is_merge, has_hotfix_marker FROM commits`).Scan(
			&got.SHA, &got.Repo, &got.AuthorHandle, &got.CommitterHandle, &aAt, &cAt,
			&got.Additions, &got.Deletions, &got.FilesChanged, &got.MessageSubject,
			&aBot, &cBot, &sigVer, &landed, &got.RevertsSHA, &isRev, &isMerge, &hotfix,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.AuthoredAt = mustParseTime(t, aAt)
		got.CommittedAt = mustParseTime(t, cAt)
		got.AuthorIsBot = aBot == 1
		got.CommitterIsBot = cBot == 1
		sigBool := sigVer == 1
		got.SignatureVerified = &sigBool
		landedBool := landed == 1
		got.LandedViaPR = &landedBool
		got.IsRevert = isRev == 1
		got.IsMerge = isMerge == 1
		got.HasHotfixMarker = hotfix == 1
		if got.SHA != "sha-c" || got.Repo != "kmcd/repo-1" || got.AuthorHandle != "alice" || got.CommitterHandle != "bob" ||
			got.Additions != 10 || got.Deletions != 5 || got.FilesChanged != 3 || got.MessageSubject != "feat: add x" ||
			got.AuthorIsBot || got.CommitterIsBot || !*got.SignatureVerified || !*got.LandedViaPR ||
			got.RevertsSHA != "sha-prev" || got.IsRevert || got.IsMerge || !got.HasHotfixMarker ||
			!got.AuthoredAt.Equal(now) || !got.CommittedAt.Equal(later) {
			t.Errorf("commit mismatch: %+v", got)
		}
	})

	t.Run("commit_files", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO commit_files (commit_sha, repo, path, additions, deletions, change_type, prev_path) VALUES (?,?,?,?,?,?,?)`,
			"sha-c", "kmcd/repo-1", "main.go", 4, 2, "M", "old.go")
		var got model.CommitFile
		if err := db.QueryRow(`SELECT commit_sha, repo, path, additions, deletions, change_type, prev_path FROM commit_files`).Scan(
			&got.CommitSHA, &got.Repo, &got.Path, &got.Additions, &got.Deletions, &got.ChangeType, &got.PrevPath,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := model.CommitFile{CommitSHA: "sha-c", Repo: "kmcd/repo-1", Path: "main.go", Additions: 4, Deletions: 2, ChangeType: "M", PrevPath: "old.go"}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("commit_coauthors", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO commit_coauthors (commit_sha, repo, handle, source, kind) VALUES (?,?,?,?,?)`,
			"sha-c", "kmcd/repo-1", "carol", "trailer", "human")
		var got model.CommitCoauthor
		if err := db.QueryRow(`SELECT commit_sha, repo, handle, source, kind FROM commit_coauthors`).Scan(
			&got.CommitSHA, &got.Repo, &got.Handle, &got.Source, &got.Kind,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := model.CommitCoauthor{CommitSHA: "sha-c", Repo: "kmcd/repo-1", Handle: "carol", Source: "trailer", Kind: "human"}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("prs", func(t *testing.T) {
		db := openMemDB(t)
		mergedStr := laterStr
		closedStr := laterStr
		readyStr := nowStr
		firstStr := laterStr
		mustExec(t, db,
			`INSERT INTO prs (number, repo, title, opened_at, merged_at, closed_at, author_handle, additions, deletions, files_changed, base_branch, head_sha, merge_sha, merge_method, is_draft, ready_for_review_at, first_review_at, commit_count, head_repo, force_pushed_after_review, body_length, template_match, checklist_total, checklist_checked, has_risk_marker, code_block_count, image_count, link_count, issue_refs_count) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			42, "kmcd/repo-1", "feat: add y", nowStr, mergedStr, closedStr, "alice",
			11, 6, 4, "main", "sha-head", "sha-merge", "squash", 0,
			readyStr, firstStr, 3, "kmcd/fork", 1,
			128, 0.75, 5, 3, 1, 2, 1, 4, 2,
		)
		var got model.PR
		var openedAt, mergedAt, closedAt, readyAt, firstAt string
		var isDraft, forced, hasRisk int
		var tmpl float64
		if err := db.QueryRow(`SELECT number, repo, title, opened_at, merged_at, closed_at, author_handle, additions, deletions, files_changed, base_branch, head_sha, merge_sha, merge_method, is_draft, ready_for_review_at, first_review_at, commit_count, head_repo, force_pushed_after_review, body_length, template_match, checklist_total, checklist_checked, has_risk_marker, code_block_count, image_count, link_count, issue_refs_count FROM prs`).Scan(
			&got.Number, &got.Repo, &got.Title, &openedAt, &mergedAt, &closedAt, &got.AuthorHandle,
			&got.Additions, &got.Deletions, &got.FilesChanged,
			&got.BaseBranch, &got.HeadSHA, &got.MergeSHA, &got.MergeMethod,
			&isDraft, &readyAt, &firstAt, &got.CommitCount, &got.HeadRepo, &forced,
			&got.BodyLength, &tmpl, &got.ChecklistTotal, &got.ChecklistChecked, &hasRisk,
			&got.CodeBlockCount, &got.ImageCount, &got.LinkCount, &got.IssueRefsCount,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.OpenedAt = mustParseTime(t, openedAt)
		m := mustParseTime(t, mergedAt)
		c := mustParseTime(t, closedAt)
		r := mustParseTime(t, readyAt)
		f := mustParseTime(t, firstAt)
		got.MergedAt, got.ClosedAt, got.ReadyForReviewAt, got.FirstReviewAt = &m, &c, &r, &f
		got.IsDraft = isDraft == 1
		got.ForcePushedAfterReview = forced == 1
		got.HasRiskMarker = hasRisk == 1
		got.TemplateMatch = &tmpl
		if got.Number != 42 || got.Repo != "kmcd/repo-1" || got.Title != "feat: add y" ||
			got.AuthorHandle != "alice" || got.Additions != 11 || got.Deletions != 6 || got.FilesChanged != 4 ||
			got.BaseBranch != "main" || got.HeadSHA != "sha-head" || got.MergeSHA != "sha-merge" ||
			got.MergeMethod != "squash" || got.IsDraft || got.CommitCount != 3 || got.HeadRepo != "kmcd/fork" ||
			!got.ForcePushedAfterReview || got.BodyLength != 128 || *got.TemplateMatch != 0.75 ||
			got.ChecklistTotal != 5 || got.ChecklistChecked != 3 || !got.HasRiskMarker ||
			got.CodeBlockCount != 2 || got.ImageCount != 1 || got.LinkCount != 4 || got.IssueRefsCount != 2 ||
			!got.OpenedAt.Equal(now) || !got.MergedAt.Equal(later) || !got.ClosedAt.Equal(later) ||
			!got.ReadyForReviewAt.Equal(now) || !got.FirstReviewAt.Equal(later) {
			t.Errorf("PR mismatch: %+v", got)
		}
	})

	t.Run("pr_commits", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO pr_commits (pr_number, repo, sha) VALUES (?,?,?)`,
			42, "kmcd/repo-1", "sha-c")
		var got model.PRCommit
		if err := db.QueryRow(`SELECT pr_number, repo, sha FROM pr_commits`).Scan(&got.PRNumber, &got.Repo, &got.SHA); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := model.PRCommit{PRNumber: 42, Repo: "kmcd/repo-1", SHA: "sha-c"}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("reviews", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO reviews (pr_number, repo, reviewer_handle, submitted_at, state, body_length) VALUES (?,?,?,?,?,?)`,
			42, "kmcd/repo-1", "bob", nowStr, "APPROVED", 64)
		var got model.Review
		var ts string
		if err := db.QueryRow(`SELECT pr_number, repo, reviewer_handle, submitted_at, state, body_length FROM reviews`).Scan(
			&got.PRNumber, &got.Repo, &got.ReviewerHandle, &ts, &got.State, &got.BodyLength,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.SubmittedAt = mustParseTime(t, ts)
		want := model.Review{PRNumber: 42, Repo: "kmcd/repo-1", ReviewerHandle: "bob", SubmittedAt: got.SubmittedAt, State: "APPROVED", BodyLength: 64}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("pr_comments", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO pr_comments (pr_number, repo, author_handle, author_is_bot, created_at, kind, body_length, in_reply_to, path) VALUES (?,?,?,?,?,?,?,?,?)`,
			42, "kmcd/repo-1", "carol", 0, nowStr, "review_comment", 32, int64(99), "main.go")
		var got model.PRComment
		var ts string
		var bot int
		var inReply int64
		if err := db.QueryRow(`SELECT pr_number, repo, author_handle, author_is_bot, created_at, kind, body_length, in_reply_to, path FROM pr_comments`).Scan(
			&got.PRNumber, &got.Repo, &got.AuthorHandle, &bot, &ts, &got.Kind, &got.BodyLength, &inReply, &got.Path,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.AuthorIsBot = bot == 1
		got.CreatedAt = mustParseTime(t, ts)
		got.InReplyTo = &inReply
		if got.PRNumber != 42 || got.Repo != "kmcd/repo-1" || got.AuthorHandle != "carol" ||
			got.AuthorIsBot || got.Kind != "review_comment" || got.BodyLength != 32 ||
			*got.InReplyTo != 99 || got.Path != "main.go" || !got.CreatedAt.Equal(now) {
			t.Errorf("PRComment mismatch: %+v", got)
		}
	})

	t.Run("pr_review_requests", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO pr_review_requests (pr_number, repo, requested_handle, requested_type, requested_at) VALUES (?,?,?,?,?)`,
			42, "kmcd/repo-1", "team-x", "team", nowStr)
		var got model.PRReviewRequest
		var ts string
		if err := db.QueryRow(`SELECT pr_number, repo, requested_handle, requested_type, requested_at FROM pr_review_requests`).Scan(
			&got.PRNumber, &got.Repo, &got.RequestedHandle, &got.RequestedType, &ts,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.RequestedAt = mustParseTime(t, ts)
		want := model.PRReviewRequest{PRNumber: 42, Repo: "kmcd/repo-1", RequestedHandle: "team-x", RequestedType: "team", RequestedAt: got.RequestedAt}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("pr_labels", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO pr_labels (pr_number, repo, label) VALUES (?,?,?)`, 42, "kmcd/repo-1", "bug")
		var got model.PRLabel
		if err := db.QueryRow(`SELECT pr_number, repo, label FROM pr_labels`).Scan(&got.PRNumber, &got.Repo, &got.Label); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want := model.PRLabel{PRNumber: 42, Repo: "kmcd/repo-1", Label: "bug"}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("builds", func(t *testing.T) {
		db := openMemDB(t)
		dur := 90
		prNum := 42
		mustExec(t, db,
			`INSERT INTO builds (id, repo, source, pipeline, status, conclusion, started_at, completed_at, duration_seconds, commit_sha, branch, event, attempt, rerun_of_id, created_at, pr_number) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"build-1", "kmcd/repo-1", "github_actions", "ci", "completed", "success",
			nowStr, laterStr, dur, "sha-c", "main", "push", 2, "build-0", nowStr, prNum,
		)
		var got model.Build
		var startedAt, completedAt, createdAt string
		var d, pr int
		if err := db.QueryRow(`SELECT id, repo, source, pipeline, status, conclusion, started_at, completed_at, duration_seconds, commit_sha, branch, event, attempt, rerun_of_id, created_at, pr_number FROM builds`).Scan(
			&got.ID, &got.Repo, &got.Source, &got.Pipeline, &got.Status, &got.Conclusion,
			&startedAt, &completedAt, &d, &got.CommitSHA, &got.Branch, &got.Event,
			&got.Attempt, &got.RerunOfID, &createdAt, &pr,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		s := mustParseTime(t, startedAt)
		c := mustParseTime(t, completedAt)
		got.StartedAt, got.CompletedAt = &s, &c
		got.DurationSeconds = &d
		got.CreatedAt = mustParseTime(t, createdAt)
		got.PRNumber = &pr
		if got.ID != "build-1" || got.Repo != "kmcd/repo-1" || got.Source != "github_actions" ||
			got.Pipeline != "ci" || got.Status != "completed" || got.Conclusion != "success" ||
			got.CommitSHA != "sha-c" || got.Branch != "main" || got.Event != "push" ||
			got.Attempt != 2 || got.RerunOfID != "build-0" || *got.DurationSeconds != 90 ||
			*got.PRNumber != 42 || !got.StartedAt.Equal(now) || !got.CompletedAt.Equal(later) ||
			!got.CreatedAt.Equal(now) {
			t.Errorf("Build mismatch: %+v", got)
		}
	})

	t.Run("build_jobs", func(t *testing.T) {
		db := openMemDB(t)
		dur := 30
		mustExec(t, db, `INSERT INTO build_jobs (build_id, repo, name, status, conclusion, duration_seconds, attempt) VALUES (?,?,?,?,?,?,?)`,
			"build-1", "kmcd/repo-1", "test", "completed", "success", dur, 3)
		var got model.BuildJob
		var d int
		if err := db.QueryRow(`SELECT build_id, repo, name, status, conclusion, duration_seconds, attempt FROM build_jobs`).Scan(
			&got.BuildID, &got.Repo, &got.Name, &got.Status, &got.Conclusion, &d, &got.Attempt,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.DurationSeconds = &d
		if got.BuildID != "build-1" || got.Repo != "kmcd/repo-1" || got.Name != "test" ||
			got.Status != "completed" || got.Conclusion != "success" || *got.DurationSeconds != 30 ||
			got.Attempt != 3 {
			t.Errorf("BuildJob mismatch: %+v", got)
		}
	})

	t.Run("deploys", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO deploys (id, repo, environment, deployed_at, commit_sha, source, status, supersedes_deploy_id, rolled_back, trigger, release_tag, version) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			"dep-1", "kmcd/repo-1", "production", nowStr, "sha-c", "github",
			"success", "dep-0", 1, "manual", "v1.0.0", "build-42",
		)
		var got model.Deploy
		var ts string
		var rolled int
		if err := db.QueryRow(`SELECT id, repo, environment, deployed_at, commit_sha, source, status, supersedes_deploy_id, rolled_back, trigger, release_tag, version FROM deploys`).Scan(
			&got.ID, &got.Repo, &got.Environment, &ts, &got.CommitSHA, &got.Source,
			&got.Status, &got.SupersedesDeployID, &rolled, &got.Trigger, &got.ReleaseTag, &got.Version,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.DeployedAt = mustParseTime(t, ts)
		got.RolledBack = rolled == 1
		want := model.Deploy{
			ID: "dep-1", Repo: "kmcd/repo-1", Environment: "production",
			DeployedAt: got.DeployedAt, CommitSHA: "sha-c", Source: "github",
			Status: "success", SupersedesDeployID: "dep-0", RolledBack: true,
			Trigger: "manual", ReleaseTag: "v1.0.0", Version: "build-42",
		}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("releases", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO releases (repo, tag, name, created_at, sha, is_prerelease) VALUES (?,?,?,?,?,?)`,
			"kmcd/repo-1", "v1.0.0", "v1.0.0", nowStr, "sha-r", 0)
		var got model.Release
		var ts string
		var pre int
		if err := db.QueryRow(`SELECT repo, tag, name, created_at, sha, is_prerelease FROM releases`).Scan(
			&got.Repo, &got.Tag, &got.Name, &ts, &got.SHA, &pre,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.CreatedAt = mustParseTime(t, ts)
		got.IsPrerelease = pre == 1
		want := model.Release{Repo: "kmcd/repo-1", Tag: "v1.0.0", Name: "v1.0.0", CreatedAt: got.CreatedAt, SHA: "sha-r", IsPrerelease: false}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("incidents", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO incidents (id, repo, source, opened_at, resolved_at, severity, occurrences, release_ref, deploy_id, commit_sha, acknowledged_at, is_regression, culprit_ref) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"inc-1", "kmcd/repo-1", "sentry", nowStr, laterStr, "high", 7,
			"v1.0.0", "dep-1", "sha-c", laterStr, 1, "culprit-x",
		)
		var got model.Incident
		var openedAt, resolvedAt, ackAt string
		var reg int
		if err := db.QueryRow(`SELECT id, repo, source, opened_at, resolved_at, severity, occurrences, release_ref, deploy_id, commit_sha, acknowledged_at, is_regression, culprit_ref FROM incidents`).Scan(
			&got.ID, &got.Repo, &got.Source, &openedAt, &resolvedAt, &got.Severity, &got.Occurrences,
			&got.ReleaseRef, &got.DeployID, &got.CommitSHA, &ackAt, &reg, &got.CulpritRef,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.OpenedAt = mustParseTime(t, openedAt)
		r := mustParseTime(t, resolvedAt)
		a := mustParseTime(t, ackAt)
		got.ResolvedAt, got.AcknowledgedAt = &r, &a
		got.IsRegression = reg == 1
		if got.ID != "inc-1" || got.Repo != "kmcd/repo-1" || got.Source != "sentry" ||
			got.Severity != "high" || got.Occurrences != 7 || got.ReleaseRef != "v1.0.0" ||
			got.DeployID != "dep-1" || got.CommitSHA != "sha-c" || !got.IsRegression ||
			got.CulpritRef != "culprit-x" || !got.OpenedAt.Equal(now) ||
			!got.ResolvedAt.Equal(later) || !got.AcknowledgedAt.Equal(later) {
			t.Errorf("Incident mismatch: %+v", got)
		}
	})

	t.Run("defects", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO defects (id, repo, ticket_ref, source, opened_at, closed_at) VALUES (?,?,?,?,?,?)`,
			"def-1", "kmcd/repo-1", "JIRA-123", "pr_title", nowStr, laterStr)
		var got model.Defect
		var openedAt, closedAt string
		if err := db.QueryRow(`SELECT id, repo, ticket_ref, source, opened_at, closed_at FROM defects`).Scan(
			&got.ID, &got.Repo, &got.TicketRef, &got.Source, &openedAt, &closedAt,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.OpenedAt = mustParseTime(t, openedAt)
		c := mustParseTime(t, closedAt)
		got.ClosedAt = &c
		if got.ID != "def-1" || got.Repo != "kmcd/repo-1" || got.TicketRef != "JIRA-123" ||
			got.Source != "pr_title" || !got.OpenedAt.Equal(now) || !got.ClosedAt.Equal(later) {
			t.Errorf("Defect mismatch: %+v", got)
		}
	})

	t.Run("file_metrics", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO file_metrics (repo, path, snapshot_sha, language, is_binary, is_generated, is_vendored, is_test, is_dependency_manifest, size_bytes, loc_total, loc_code, loc_blank, max_indent, mean_indent, max_line_length, p95_line_length) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"kmcd/repo-1", "main.go", "sha-snap", "Go", 0, 0, 0, 0, 0,
			int64(2048), 100, 80, 10, 5, 2.5, 120, 100,
		)
		var got model.FileMetric
		var bin, gen, vend, isTest, depMan int
		if err := db.QueryRow(`SELECT repo, path, snapshot_sha, language, is_binary, is_generated, is_vendored, is_test, is_dependency_manifest, size_bytes, loc_total, loc_code, loc_blank, max_indent, mean_indent, max_line_length, p95_line_length FROM file_metrics`).Scan(
			&got.Repo, &got.Path, &got.SnapshotSHA, &got.Language,
			&bin, &gen, &vend, &isTest, &depMan,
			&got.SizeBytes, &got.LOCTotal, &got.LOCCode, &got.LOCBlank,
			&got.MaxIndent, &got.MeanIndent, &got.MaxLineLength, &got.P95LineLength,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.IsBinary = bin == 1
		got.IsGenerated = gen == 1
		got.IsVendored = vend == 1
		got.IsTest = isTest == 1
		got.IsDependencyManifest = depMan == 1
		want := model.FileMetric{
			Repo: "kmcd/repo-1", Path: "main.go", SnapshotSHA: "sha-snap", Language: "Go",
			SizeBytes: 2048, LOCTotal: 100, LOCCode: 80, LOCBlank: 10,
			MaxIndent: 5, MeanIndent: 2.5, MaxLineLength: 120, P95LineLength: 100,
		}
		if got != want {
			t.Errorf("got %+v want %+v", got, want)
		}
	})

	t.Run("harness_artifacts", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO harness_artifacts (repo, path, tool, kind, line_count, first_seen_commit, first_seen_at, last_modified_at, content) VALUES (?,?,?,?,?,?,?,?,?)`,
			"kmcd/repo-1", "CLAUDE.md", "claude", "guide", 50, "sha-fs", nowStr, laterStr, "# Project",
		)
		var got model.HarnessArtifact
		var fs, lm string
		if err := db.QueryRow(`SELECT repo, path, tool, kind, line_count, first_seen_commit, first_seen_at, last_modified_at, content FROM harness_artifacts`).Scan(
			&got.Repo, &got.Path, &got.Tool, &got.Kind, &got.LineCount,
			&got.FirstSeenCommit, &fs, &lm, &got.Content,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got.FirstSeenAt = mustParseTime(t, fs)
		got.LastModifiedAt = mustParseTime(t, lm)
		if got.Repo != "kmcd/repo-1" || got.Path != "CLAUDE.md" || got.Tool != "claude" ||
			got.Kind != "guide" || got.LineCount != 50 || got.FirstSeenCommit != "sha-fs" ||
			got.Content != "# Project" || !got.FirstSeenAt.Equal(now) ||
			!got.LastModifiedAt.Equal(later) {
			t.Errorf("HarnessArtifact mismatch: %+v", got)
		}
	})
}

func TestDDL_NullablePointersStayNull(t *testing.T) {
	now := fixtureTime()
	nowStr := rfc(now)

	t.Run("repos_created_at_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO repos (slug, default_branch, head_sha, team) VALUES (?,?,?,?)`,
			"kmcd/r", "main", "sha", "team")
		var createdAt sql.NullString
		if err := db.QueryRow(`SELECT created_at FROM repos`).Scan(&createdAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if createdAt.Valid {
			t.Errorf("expected NULL created_at, got %q", createdAt.String)
		}
	})

	t.Run("branch_protection_required_reviews_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db, `INSERT INTO branch_protection (repo, branch) VALUES (?,?)`, "kmcd/r", "main")
		var rr sql.NullInt64
		if err := db.QueryRow(`SELECT required_reviews FROM branch_protection`).Scan(&rr); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if rr.Valid {
			t.Errorf("expected NULL required_reviews, got %d", rr.Int64)
		}
	})

	t.Run("commits_signature_and_landed_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO commits (sha, repo, authored_at, committed_at) VALUES (?,?,?,?)`,
			"sha-c", "kmcd/r", nowStr, nowStr)
		var sig, landed sql.NullInt64
		if err := db.QueryRow(`SELECT signature_verified, landed_via_pr FROM commits`).Scan(&sig, &landed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if sig.Valid {
			t.Errorf("expected NULL signature_verified, got %d", sig.Int64)
		}
		if landed.Valid {
			t.Errorf("expected NULL landed_via_pr, got %d", landed.Int64)
		}
	})

	t.Run("prs_merged_closed_template_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO prs (number, repo, opened_at) VALUES (?,?,?)`,
			1, "kmcd/r", nowStr)
		var merged, closed, ready, first sql.NullString
		var tmpl sql.NullFloat64
		if err := db.QueryRow(`SELECT merged_at, closed_at, ready_for_review_at, first_review_at, template_match FROM prs`).Scan(
			&merged, &closed, &ready, &first, &tmpl,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if merged.Valid || closed.Valid || ready.Valid || first.Valid {
			t.Errorf("expected null timestamps; got merged=%v closed=%v ready=%v first=%v",
				merged, closed, ready, first)
		}
		if tmpl.Valid {
			t.Errorf("expected NULL template_match, got %f", tmpl.Float64)
		}
	})

	t.Run("pr_comments_in_reply_to_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO pr_comments (pr_number, repo, created_at, kind) VALUES (?,?,?,?)`,
			1, "kmcd/r", nowStr, "issue_comment")
		var inReply sql.NullInt64
		if err := db.QueryRow(`SELECT in_reply_to FROM pr_comments`).Scan(&inReply); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if inReply.Valid {
			t.Errorf("expected NULL in_reply_to, got %d", inReply.Int64)
		}
	})

	t.Run("builds_optional_columns_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO builds (id, repo, source, created_at) VALUES (?,?,?,?)`,
			"b-1", "kmcd/r", "github_actions", nowStr)
		var started, completed sql.NullString
		var dur, pr sql.NullInt64
		if err := db.QueryRow(`SELECT started_at, completed_at, duration_seconds, pr_number FROM builds`).Scan(
			&started, &completed, &dur, &pr,
		); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if started.Valid || completed.Valid || dur.Valid || pr.Valid {
			t.Errorf("expected NULLs; got started=%v completed=%v dur=%v pr=%v",
				started, completed, dur, pr)
		}
	})

	t.Run("incidents_optional_timestamps_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO incidents (id, repo, source, opened_at) VALUES (?,?,?,?)`,
			"i-1", "kmcd/r", "sentry", nowStr)
		var resolved, ack sql.NullString
		if err := db.QueryRow(`SELECT resolved_at, acknowledged_at FROM incidents`).Scan(&resolved, &ack); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if resolved.Valid || ack.Valid {
			t.Errorf("expected NULLs; got resolved=%v ack=%v", resolved, ack)
		}
	})

	t.Run("defects_closed_at_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO defects (id, repo, ticket_ref, source, opened_at) VALUES (?,?,?,?,?)`,
			"d-1", "kmcd/r", "T-1", "pr_title", nowStr)
		var closed sql.NullString
		if err := db.QueryRow(`SELECT closed_at FROM defects`).Scan(&closed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if closed.Valid {
			t.Errorf("expected NULL closed_at, got %q", closed.String)
		}
	})

	t.Run("build_jobs_duration_null", func(t *testing.T) {
		db := openMemDB(t)
		mustExec(t, db,
			`INSERT INTO build_jobs (build_id, repo, name) VALUES (?,?,?)`,
			"b-1", "kmcd/r", "test")
		var dur sql.NullInt64
		if err := db.QueryRow(`SELECT duration_seconds FROM build_jobs`).Scan(&dur); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if dur.Valid {
			t.Errorf("expected NULL duration_seconds, got %d", dur.Int64)
		}
	})
}

// snakeCase lowercases the field name with underscores between word boundaries:
// runs of uppercase letters (acronyms like SHA, PR, LOC) stay together but
// emit a single underscore when transitioning to/from non-uppercase. Special
// short forms in the model are normalised through structToColumn.
func snakeCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			// Lower/digit -> upper: boundary.
			if (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(r) {
				b.WriteByte('_')
			}
			// Upper -> Upper followed by Lower: boundary before the upper
			// (e.g. "SHAValue" -> "sha_value").
			if i+1 < len(runes) && unicode.IsUpper(prev) && unicode.IsUpper(r) && unicode.IsLower(runes[i+1]) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// fieldOverrides maps struct field names to their actual column name when the
// snake_case heuristic would get the wrong answer for an initialism.
var fieldOverrides = map[string]string{
	"SHA":                "sha",
	"HeadSHA":            "head_sha",
	"LastCommitSHA":      "last_commit_sha",
	"CommitSHA":          "commit_sha",
	"MergeSHA":           "merge_sha",
	"RevertsSHA":         "reverts_sha",
	"SnapshotSHA":        "snapshot_sha",
	"PRNumber":           "pr_number",
	"PRsInWindow":        "prs_in_window",
	"PRsAllTime":         "prs_all_time",
	"LOCTotal":           "loc_total",
	"LOCCode":            "loc_code",
	"LOCBlank":           "loc_blank",
	"P95LineLength":      "p95_line_length",
	"BuildID":            "build_id",
	"SupersedesDeployID": "supersedes_deploy_id",
	"DeployID":           "deploy_id",
	"RerunOfID":          "rerun_of_id",
	"ID":                 "id",
	"LandedViaPR":        "landed_via_pr",
}

func structToColumn(field string) string {
	if c, ok := fieldOverrides[field]; ok {
		return c
	}
	return snakeCase(field)
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[n] = true
	}
	return cols
}

// TestDDL_NoUnknownColumns walks each canonical struct via reflection and
// asserts every field maps to a column in the corresponding table. This
// catches a struct field added without a matching DDL column.
func TestDDL_NoUnknownColumns(t *testing.T) {
	db := openMemDB(t)

	cases := []struct {
		table string
		zero  any
	}{
		{"repos", model.Repo{}},
		{"repo_languages", model.RepoLanguage{}},
		{"branches", model.Branch{}},
		{"branch_protection", model.BranchProtection{}},
		{"codeowners", model.Codeowner{}},
		{"commits", model.Commit{}},
		{"commit_files", model.CommitFile{}},
		{"commit_coauthors", model.CommitCoauthor{}},
		{"prs", model.PR{}},
		{"pr_commits", model.PRCommit{}},
		{"reviews", model.Review{}},
		{"pr_comments", model.PRComment{}},
		{"pr_review_requests", model.PRReviewRequest{}},
		{"pr_labels", model.PRLabel{}},
		{"builds", model.Build{}},
		{"build_jobs", model.BuildJob{}},
		{"deploys", model.Deploy{}},
		{"releases", model.Release{}},
		{"incidents", model.Incident{}},
		{"defects", model.Defect{}},
		{"file_metrics", model.FileMetric{}},
		{"harness_artifacts", model.HarnessArtifact{}},
		{"file_complexity_history", model.FileComplexityHistory{}},
	}

	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			cols := tableColumns(t, db, c.table)
			if len(cols) == 0 {
				t.Fatalf("no columns found for table %s", c.table)
			}
			rt := reflect.TypeOf(c.zero)
			for i := 0; i < rt.NumField(); i++ {
				field := rt.Field(i).Name
				col := structToColumn(field)
				if !cols[col] {
					// Build a sorted column list for the error message.
					names := make([]string, 0, len(cols))
					for k := range cols {
						names = append(names, k)
					}
					sort.Strings(names)
					t.Errorf("struct field %s.%s maps to column %q which is missing from table %s (have: %v)",
						rt.Name(), field, col, c.table, names)
				}
			}
		})
	}
}
