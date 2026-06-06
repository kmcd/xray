package postprocess_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kmcd/xray/internal/model"
	"github.com/kmcd/xray/internal/postprocess"
	"github.com/kmcd/xray/internal/store"
)

func newTestStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.sqlite")
	st, err := store.Open(path, "0.0.0-test")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, st.DB()
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tt
}

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestIncidentLinkage_DeployMatchByReleaseTag(t *testing.T) {
	st, db := newTestStore(t)

	if err := st.InsertDeploy(model.Deploy{
		ID:          "dep-1",
		Repo:        "kmcd/foo",
		Environment: "production",
		DeployedAt:  mustTime(t, "2025-01-02T00:00:00Z"),
		CommitSHA:   "aaaa",
		Source:      "github",
		Status:      "success",
		ReleaseTag:  "v1.2.3",
	}); err != nil {
		t.Fatalf("InsertDeploy: %v", err)
	}
	if err := st.InsertIncident(model.Incident{
		ID:         "inc-1",
		Repo:       "kmcd/foo",
		Source:     "sentry",
		OpenedAt:   mustTime(t, "2025-01-03T00:00:00Z"),
		ReleaseRef: "v1.2.3",
	}); err != nil {
		t.Fatalf("InsertIncident: %v", err)
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.IncidentsLinked != 1 {
		t.Errorf("IncidentsLinked = %d, want 1", stats.IncidentsLinked)
	}

	var depID, sha string
	if err := db.QueryRow(
		`SELECT COALESCE(deploy_id,''), COALESCE(commit_sha,'') FROM incidents WHERE id = ?`,
		"inc-1",
	).Scan(&depID, &sha); err != nil {
		t.Fatalf("query incident: %v", err)
	}
	if depID != "dep-1" {
		t.Errorf("deploy_id = %q, want dep-1", depID)
	}
	if sha != "aaaa" {
		t.Errorf("commit_sha = %q, want aaaa", sha)
	}
}

func TestIncidentLinkage_DeployMatchByVersion(t *testing.T) {
	st, db := newTestStore(t)

	if err := st.InsertDeploy(model.Deploy{
		ID:          "dep-2",
		Repo:        "kmcd/foo",
		Environment: "production",
		DeployedAt:  mustTime(t, "2025-01-02T00:00:00Z"),
		CommitSHA:   "bbbb",
		Source:      "honeycomb",
		Status:      "success",
		Version:     "build-42",
	}); err != nil {
		t.Fatalf("InsertDeploy: %v", err)
	}
	if err := st.InsertIncident(model.Incident{
		ID:         "inc-2",
		Repo:       "kmcd/foo",
		Source:     "sentry",
		OpenedAt:   mustTime(t, "2025-01-03T00:00:00Z"),
		ReleaseRef: "build-42",
	}); err != nil {
		t.Fatalf("InsertIncident: %v", err)
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.IncidentsLinked != 1 {
		t.Errorf("IncidentsLinked = %d, want 1", stats.IncidentsLinked)
	}

	var depID, sha string
	if err := db.QueryRow(
		`SELECT COALESCE(deploy_id,''), COALESCE(commit_sha,'') FROM incidents WHERE id = ?`,
		"inc-2",
	).Scan(&depID, &sha); err != nil {
		t.Fatalf("query incident: %v", err)
	}
	if depID != "dep-2" || sha != "bbbb" {
		t.Errorf("got (%q, %q), want (dep-2, bbbb)", depID, sha)
	}
}

func TestIncidentLinkage_FallbackToReleaseSHA(t *testing.T) {
	st, db := newTestStore(t)

	if err := st.InsertRelease(model.Release{
		Repo:      "kmcd/foo",
		Tag:       "v9.9.9",
		Name:      "fallback",
		CreatedAt: mustTime(t, "2025-01-01T00:00:00Z"),
		SHA:       "cccc",
	}); err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if err := st.InsertIncident(model.Incident{
		ID:         "inc-3",
		Repo:       "kmcd/foo",
		Source:     "sentry",
		OpenedAt:   mustTime(t, "2025-01-03T00:00:00Z"),
		ReleaseRef: "v9.9.9",
	}); err != nil {
		t.Fatalf("InsertIncident: %v", err)
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.IncidentsLinked != 1 {
		t.Errorf("IncidentsLinked = %d, want 1", stats.IncidentsLinked)
	}

	var depID, sha string
	if err := db.QueryRow(
		`SELECT COALESCE(deploy_id,''), COALESCE(commit_sha,'') FROM incidents WHERE id = ?`,
		"inc-3",
	).Scan(&depID, &sha); err != nil {
		t.Fatalf("query incident: %v", err)
	}
	if depID != "" {
		t.Errorf("deploy_id = %q, want empty", depID)
	}
	if sha != "cccc" {
		t.Errorf("commit_sha = %q, want cccc", sha)
	}
}

func TestIncidentLinkage_NoMatchLeavesIncidentAlone(t *testing.T) {
	st, db := newTestStore(t)

	if err := st.InsertIncident(model.Incident{
		ID:         "inc-4",
		Repo:       "kmcd/foo",
		Source:     "sentry",
		OpenedAt:   mustTime(t, "2025-01-03T00:00:00Z"),
		ReleaseRef: "never-shipped",
	}); err != nil {
		t.Fatalf("InsertIncident: %v", err)
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.IncidentsLinked != 0 {
		t.Errorf("IncidentsLinked = %d, want 0", stats.IncidentsLinked)
	}

	var depID, sha string
	if err := db.QueryRow(
		`SELECT COALESCE(deploy_id,''), COALESCE(commit_sha,'') FROM incidents WHERE id = ?`,
		"inc-4",
	).Scan(&depID, &sha); err != nil {
		t.Fatalf("query incident: %v", err)
	}
	if depID != "" || sha != "" {
		t.Errorf("expected unlinked, got deploy_id=%q sha=%q", depID, sha)
	}
}

func TestDeployRollbackLinkage(t *testing.T) {
	st, db := newTestStore(t)

	// Three deploys in the same env. The third re-deploys the first
	// commit while skipping the second, and the second failed: classified
	// as a rollback per ADR 017.
	deploys := []model.Deploy{
		{
			ID: "d-A", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-01T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
		{
			ID: "d-B", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-02T00:00:00Z"),
			CommitSHA:  "sha-B", Source: "github", Status: "failed",
		},
		{
			ID: "d-C", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-03T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
	}
	for _, d := range deploys {
		if err := st.InsertDeploy(d); err != nil {
			t.Fatalf("InsertDeploy %s: %v", d.ID, err)
		}
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.DeploysRolledBack != 1 {
		t.Errorf("DeploysRolledBack = %d, want 1", stats.DeploysRolledBack)
	}

	// d-C should now supersede d-B.
	var supersedes string
	if err := db.QueryRow(
		`SELECT COALESCE(supersedes_deploy_id,'') FROM deploys WHERE id = ?`, "d-C",
	).Scan(&supersedes); err != nil {
		t.Fatalf("query d-C: %v", err)
	}
	if supersedes != "d-B" {
		t.Errorf("d-C.supersedes_deploy_id = %q, want d-B", supersedes)
	}

	// d-B should be marked rolled_back.
	var rolled int
	if err := db.QueryRow(
		`SELECT rolled_back FROM deploys WHERE id = ?`, "d-B",
	).Scan(&rolled); err != nil {
		t.Fatalf("query d-B: %v", err)
	}
	if rolled != 1 {
		t.Errorf("d-B.rolled_back = %d, want 1", rolled)
	}

	// d-A unchanged.
	if err := db.QueryRow(
		`SELECT rolled_back FROM deploys WHERE id = ?`, "d-A",
	).Scan(&rolled); err != nil {
		t.Fatalf("query d-A: %v", err)
	}
	if rolled != 0 {
		t.Errorf("d-A.rolled_back = %d, want 0", rolled)
	}
}

func TestDeployRollback_SkipsEmptyEnvironment(t *testing.T) {
	st, db := newTestStore(t)

	// Three deploys with no environment — should be ignored entirely
	// even though the commit pattern matches the rollback heuristic.
	deploys := []model.Deploy{
		{
			ID: "d-1", Repo: "kmcd/foo", DeployedAt: mustTime(t, "2025-01-01T00:00:00Z"),
			CommitSHA: "x", Source: "github", Status: "success",
		},
		{
			ID: "d-2", Repo: "kmcd/foo", DeployedAt: mustTime(t, "2025-01-02T00:00:00Z"),
			CommitSHA: "y", Source: "github", Status: "success",
		},
		{
			ID: "d-3", Repo: "kmcd/foo", DeployedAt: mustTime(t, "2025-01-03T00:00:00Z"),
			CommitSHA: "x", Source: "github", Status: "success",
		},
	}
	for _, d := range deploys {
		if err := st.InsertDeploy(d); err != nil {
			t.Fatalf("InsertDeploy %s: %v", d.ID, err)
		}
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.DeploysRolledBack != 0 {
		t.Errorf("DeploysRolledBack = %d, want 0", stats.DeploysRolledBack)
	}
}

// ADR 017: a re-deploy of a green commit (same SHA pattern) where the
// intervening deploy succeeded is NOT a rollback. The original heuristic
// false-positived on canary advance / blue-green flip-back; the tightened
// heuristic gates on D[i-1].status != "success".
func TestDeployRollback_PredecessorSuccessNotFlagged(t *testing.T) {
	st, db := newTestStore(t)

	deploys := []model.Deploy{
		{
			ID: "g-A", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-01T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
		{
			ID: "g-B", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-02T00:00:00Z"),
			CommitSHA:  "sha-B", Source: "github", Status: "success",
		},
		{
			ID: "g-C", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-03T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
	}
	for _, d := range deploys {
		if err := st.InsertDeploy(d); err != nil {
			t.Fatalf("InsertDeploy %s: %v", d.ID, err)
		}
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.DeploysRolledBack != 0 {
		t.Errorf("DeploysRolledBack = %d, want 0", stats.DeploysRolledBack)
	}

	var rolled int
	if err := db.QueryRow(
		`SELECT rolled_back FROM deploys WHERE id = ?`, "g-B",
	).Scan(&rolled); err != nil {
		t.Fatalf("query g-B: %v", err)
	}
	if rolled != 0 {
		t.Errorf("g-B.rolled_back = %d, want 0 (predecessor succeeded, not a rollback)", rolled)
	}

	var supersedes sql.NullString
	if err := db.QueryRow(
		`SELECT supersedes_deploy_id FROM deploys WHERE id = ?`, "g-C",
	).Scan(&supersedes); err != nil {
		t.Fatalf("query g-C: %v", err)
	}
	if supersedes.String != "" {
		t.Errorf("g-C.supersedes_deploy_id = %q, want empty", supersedes.String)
	}
}

// ADR 017: a "failed" predecessor satisfies the non-success gate and the
// rollback is flagged.
func TestDeployRollback_PredecessorFailedFlagged(t *testing.T) {
	st, db := newTestStore(t)

	deploys := []model.Deploy{
		{
			ID: "f-A", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-01T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
		{
			ID: "f-B", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-02T00:00:00Z"),
			CommitSHA:  "sha-B", Source: "github", Status: "failed",
		},
		{
			ID: "f-C", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-03T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
	}
	for _, d := range deploys {
		if err := st.InsertDeploy(d); err != nil {
			t.Fatalf("InsertDeploy %s: %v", d.ID, err)
		}
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.DeploysRolledBack != 1 {
		t.Errorf("DeploysRolledBack = %d, want 1", stats.DeploysRolledBack)
	}

	var supersedes string
	if err := db.QueryRow(
		`SELECT COALESCE(supersedes_deploy_id,'') FROM deploys WHERE id = ?`, "f-C",
	).Scan(&supersedes); err != nil {
		t.Fatalf("query f-C: %v", err)
	}
	if supersedes != "f-B" {
		t.Errorf("f-C.supersedes_deploy_id = %q, want f-B", supersedes)
	}

	var rolled int
	if err := db.QueryRow(
		`SELECT rolled_back FROM deploys WHERE id = ?`, "f-B",
	).Scan(&rolled); err != nil {
		t.Fatalf("query f-B: %v", err)
	}
	if rolled != 1 {
		t.Errorf("f-B.rolled_back = %d, want 1", rolled)
	}
}

// ADR 017: "error" and "rolled_back" also count as non-success and gate
// the heuristic to flag the rollback.
func TestDeployRollback_PredecessorErrorFlagged(t *testing.T) {
	st, db := newTestStore(t)

	deploys := []model.Deploy{
		{
			ID: "x-A", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-01T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
		{
			ID: "x-B", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-02T00:00:00Z"),
			CommitSHA:  "sha-B", Source: "github", Status: "error",
		},
		{
			ID: "x-C", Repo: "kmcd/foo", Environment: "production",
			DeployedAt: mustTime(t, "2025-01-03T00:00:00Z"),
			CommitSHA:  "sha-A", Source: "github", Status: "success",
		},
	}
	for _, d := range deploys {
		if err := st.InsertDeploy(d); err != nil {
			t.Fatalf("InsertDeploy %s: %v", d.ID, err)
		}
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.DeploysRolledBack != 1 {
		t.Errorf("DeploysRolledBack = %d, want 1", stats.DeploysRolledBack)
	}

	var rolled int
	if err := db.QueryRow(
		`SELECT rolled_back FROM deploys WHERE id = ?`, "x-B",
	).Scan(&rolled); err != nil {
		t.Fatalf("query x-B: %v", err)
	}
	if rolled != 1 {
		t.Errorf("x-B.rolled_back = %d, want 1 (error status is non-success)", rolled)
	}
}

func TestDeployRollback_RequiresNonEmptyCommitSHA(t *testing.T) {
	st, db := newTestStore(t)

	// Three deploys, all with empty commit_sha. No rollback should be
	// detected because the heuristic requires non-empty SHA equality.
	deploys := []model.Deploy{
		{
			ID: "e-1", Repo: "kmcd/foo", Environment: "staging",
			DeployedAt: mustTime(t, "2025-01-01T00:00:00Z"),
			Source:     "github", Status: "success",
		},
		{
			ID: "e-2", Repo: "kmcd/foo", Environment: "staging",
			DeployedAt: mustTime(t, "2025-01-02T00:00:00Z"),
			Source:     "github", Status: "success",
		},
		{
			ID: "e-3", Repo: "kmcd/foo", Environment: "staging",
			DeployedAt: mustTime(t, "2025-01-03T00:00:00Z"),
			Source:     "github", Status: "success",
		},
	}
	for _, d := range deploys {
		if err := st.InsertDeploy(d); err != nil {
			t.Fatalf("InsertDeploy %s: %v", d.ID, err)
		}
	}

	stats, err := postprocess.Run(context.Background(), db, nullLogger())
	if err != nil {
		t.Fatalf("postprocess.Run: %v", err)
	}
	if stats.DeploysRolledBack != 0 {
		t.Errorf("DeploysRolledBack = %d, want 0", stats.DeploysRolledBack)
	}
}
