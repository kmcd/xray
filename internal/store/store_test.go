package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/store"
)

func TestOpenAppliesDDLAndRecordsSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.sqlite")

	st, err := store.Open(path, "0.0.0-test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var sv int
	var tv, at string
	if err := db.QueryRowContext(t.Context(), `SELECT schema_version, tool_version, applied_at FROM _schema`).Scan(&sv, &tv, &at); err != nil {
		t.Fatalf("query _schema: %v", err)
	}
	if sv != model.SchemaVersion {
		t.Errorf("schema_version: got %d want %d", sv, model.SchemaVersion)
	}
	if tv != "0.0.0-test" {
		t.Errorf("tool_version: got %q", tv)
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("applied_at not RFC3339: %v", err)
	}
}

func TestInsertOnePerTable(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "m.sqlite"), "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	tp := func(t time.Time) *time.Time { return &t }
	ip := func(i int) *int { return &i }
	bp := func(b bool) *bool { return &b }
	fp := func(f float64) *float64 { return &f }

	if err := st.InsertRepo(model.Repo{
		Slug: "kmcd/foo", DefaultBranch: "main", HeadSHA: "abc", Team: "platform",
		PrimaryLanguage: "Go", CreatedAt: tp(now), Visibility: "private",
	}); err != nil {
		t.Errorf("InsertRepo: %v", err)
	}
	if err := st.InsertTeamRepo("platform", "kmcd/foo"); err != nil {
		t.Errorf("InsertTeamRepo: %v", err)
	}
	if err := st.InsertRepoLanguage(model.RepoLanguage{Repo: "kmcd/foo", Language: "Go", Bytes: 1234}); err != nil {
		t.Errorf("InsertRepoLanguage: %v", err)
	}
	if err := st.InsertBranch(model.Branch{Repo: "kmcd/foo", Name: "main", LastCommitSHA: "abc", LastCommitAt: now, IsDefault: true}); err != nil {
		t.Errorf("InsertBranch: %v", err)
	}
	if err := st.InsertBranchProtection(model.BranchProtection{Repo: "kmcd/foo", Branch: "main", RequiredReviews: ip(2), EnforceAdmins: true}); err != nil {
		t.Errorf("InsertBranchProtection: %v", err)
	}
	if err := st.InsertCodeowner(model.Codeowner{Repo: "kmcd/foo", Pattern: "*", OwnerHandle: "@kmcd", OwnerType: "user"}); err != nil {
		t.Errorf("InsertCodeowner: %v", err)
	}
	if err := st.InsertCommit(model.Commit{
		SHA: "abc", Repo: "kmcd/foo", AuthorHandle: "kmcd", CommitterHandle: "kmcd",
		AuthoredAt: now, CommittedAt: now, Additions: 1, Deletions: 1, FilesChanged: 1,
		MessageSubject: "init", SignatureVerified: bp(true), LandedViaPR: bp(false),
	}); err != nil {
		t.Errorf("InsertCommit: %v", err)
	}
	if err := st.InsertCommitFile(model.CommitFile{CommitSHA: "abc", Repo: "kmcd/foo", Path: "x.go", Additions: 1, Deletions: 0, ChangeType: "A"}); err != nil {
		t.Errorf("InsertCommitFile: %v", err)
	}
	if err := st.InsertCommitCoauthor(model.CommitCoauthor{CommitSHA: "abc", Repo: "kmcd/foo", Handle: "co", Source: "trailer", Kind: "human"}); err != nil {
		t.Errorf("InsertCommitCoauthor: %v", err)
	}
	if err := st.InsertPR(model.PR{Number: 1, Repo: "kmcd/foo", Title: "t", OpenedAt: now, TemplateMatch: fp(1)}); err != nil {
		t.Errorf("InsertPR: %v", err)
	}
	if err := st.InsertPRCommit(model.PRCommit{PRNumber: 1, Repo: "kmcd/foo", SHA: "abc"}); err != nil {
		t.Errorf("InsertPRCommit: %v", err)
	}
	if err := st.InsertReview(model.Review{PRNumber: 1, Repo: "kmcd/foo", ReviewerHandle: "r", SubmittedAt: now, State: "APPROVED"}); err != nil {
		t.Errorf("InsertReview: %v", err)
	}
	if err := st.InsertPRComment(model.PRComment{PRNumber: 1, Repo: "kmcd/foo", AuthorHandle: "a", CreatedAt: now, Kind: "issue_comment"}); err != nil {
		t.Errorf("InsertPRComment: %v", err)
	}
	if err := st.InsertPRReviewRequest(model.PRReviewRequest{PRNumber: 1, Repo: "kmcd/foo", RequestedHandle: "r", RequestedType: "user", RequestedAt: now}); err != nil {
		t.Errorf("InsertPRReviewRequest: %v", err)
	}
	if err := st.InsertPRLabel(model.PRLabel{PRNumber: 1, Repo: "kmcd/foo", Label: "bug"}); err != nil {
		t.Errorf("InsertPRLabel: %v", err)
	}
	if err := st.InsertBuild(model.Build{ID: "b1", Repo: "kmcd/foo", Source: "github_actions", Status: "ok", CreatedAt: now, DurationSeconds: ip(10)}); err != nil {
		t.Errorf("InsertBuild: %v", err)
	}
	if err := st.InsertBuildJob(model.BuildJob{BuildID: "b1", Repo: "kmcd/foo", Name: "test", Attempt: 1}); err != nil {
		t.Errorf("InsertBuildJob: %v", err)
	}
	if err := st.InsertDeploy(model.Deploy{ID: "d1", Repo: "kmcd/foo", Source: "github", DeployedAt: now, Status: "success"}); err != nil {
		t.Errorf("InsertDeploy: %v", err)
	}
	if err := st.InsertRelease(model.Release{Repo: "kmcd/foo", Tag: "v1", CreatedAt: now}); err != nil {
		t.Errorf("InsertRelease: %v", err)
	}
	if err := st.InsertIncident(model.Incident{ID: "i1", Repo: "kmcd/foo", Source: "sentry", OpenedAt: now}); err != nil {
		t.Errorf("InsertIncident: %v", err)
	}
	if err := st.InsertDefect(model.Defect{ID: "df1", Repo: "kmcd/foo", TicketRef: "PROJ-1", Source: "pr_title", OpenedAt: now}); err != nil {
		t.Errorf("InsertDefect: %v", err)
	}
	if err := st.InsertFileMetric(model.FileMetric{Repo: "kmcd/foo", Path: "x.go", SnapshotSHA: "abc", Language: "Go"}); err != nil {
		t.Errorf("InsertFileMetric: %v", err)
	}
	if err := st.InsertHarnessArtifact(model.HarnessArtifact{
		Repo: "kmcd/foo", Path: "CLAUDE.md", Tool: "claude_code", Kind: "instructions",
		FirstSeenCommit: "abc", FirstSeenAt: now, LastModifiedAt: now,
	}); err != nil {
		t.Errorf("InsertHarnessArtifact: %v", err)
	}
}

func TestSquashStats(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "m.sqlite"), "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	merged := now.Add(time.Hour)
	mkPR := func(num int, mergeMethod string, isMerged bool) model.PR {
		pr := model.PR{Number: num, Repo: "r", OpenedAt: now, MergeMethod: mergeMethod}
		if isMerged {
			m := merged
			pr.MergedAt = &m
		}
		return pr
	}
	// 5 merged: 3 squash, 1 merge, 1 rebase. 1 open (unmerged) — must not count.
	cases := []model.PR{
		mkPR(1, "squash", true),
		mkPR(2, "squash", true),
		mkPR(3, "squash", true),
		mkPR(4, "merge", true),
		mkPR(5, "rebase", true),
		mkPR(6, "", false),
	}
	for _, p := range cases {
		if err := st.InsertPR(p); err != nil {
			t.Fatalf("InsertPR %d: %v", p.Number, err)
		}
	}
	nSquash, nMerged, err := st.SquashStats()
	if err != nil {
		t.Fatalf("SquashStats: %v", err)
	}
	if nSquash != 3 || nMerged != 5 {
		t.Errorf("SquashStats = (%d, %d), want (3, 5)", nSquash, nMerged)
	}
}

func TestNullableTimePointer(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "m.sqlite"), "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	if err := st.InsertPR(model.PR{Number: 99, Repo: "x/y", OpenedAt: now}); err != nil {
		t.Fatalf("InsertPR: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "m.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var merged sql.NullString
	if err := db.QueryRowContext(t.Context(), `SELECT merged_at FROM prs WHERE number = 99`).Scan(&merged); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if merged.Valid {
		t.Errorf("merged_at should be NULL, got %q", merged.String)
	}
}
