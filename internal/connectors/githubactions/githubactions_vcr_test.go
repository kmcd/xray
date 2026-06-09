package githubactions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/connectors/vcr"
	"github.com/kmcd/xray/internal/model"
	"log/slog"
)

// newForTest constructs a Connector with a pre-wired HTTP client (e.g. a VCR
// recorder). Auth is handled at the transport level during recording; the
// Token field here is a non-empty placeholder to satisfy the config shape.
func newForTest(httpClient *http.Client) *Connector {
	return &Connector{
		cfg:    config.GitHubActionsConn{Token: "test-token"},
		log:    slog.Default(),
		client: github.NewClient(httpClient),
	}
}

// testWindow covers 2026-06-08 to 2026-06-10, matching the recording window.
// Hard-coded so cassette URL keys stay stable across re-runs.
var testWindow = connector.Window{
	Start: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
}

// testRepo is kmcd/xray, the repo used for recording cassettes.
var testRepo = connector.Repo{
	Slug:          "kmcd/xray",
	DefaultBranch: "main",
}

// memSink records rows emitted by the connector for assertion.
type memSink struct {
	builds    []model.Build
	buildJobs []model.BuildJob
	deploys   []model.Deploy
}

func (s *memSink) InsertBuild(b model.Build) error       { s.builds = append(s.builds, b); return nil }
func (s *memSink) InsertBuildJob(j model.BuildJob) error { s.buildJobs = append(s.buildJobs, j); return nil }
func (s *memSink) InsertDeploy(d model.Deploy) error     { s.deploys = append(s.deploys, d); return nil }

func (s *memSink) InsertRepo(model.Repo) error                               { return nil }
func (s *memSink) InsertTeamRepo(string, string) error                       { return nil }
func (s *memSink) InsertRepoLanguage(model.RepoLanguage) error               { return nil }
func (s *memSink) InsertBranch(model.Branch) error                           { return nil }
func (s *memSink) InsertBranchProtection(model.BranchProtection) error       { return nil }
func (s *memSink) InsertCodeowner(model.Codeowner) error                     { return nil }
func (s *memSink) InsertCommit(model.Commit) error                           { return nil }
func (s *memSink) InsertCommitFile(model.CommitFile) error                   { return nil }
func (s *memSink) InsertCommitCoauthor(model.CommitCoauthor) error           { return nil }
func (s *memSink) InsertPR(model.PR) error                                   { return nil }
func (s *memSink) InsertPRCommit(model.PRCommit) error                       { return nil }
func (s *memSink) InsertReview(model.Review) error                           { return nil }
func (s *memSink) InsertPRComment(model.PRComment) error                     { return nil }
func (s *memSink) InsertPRReviewRequest(model.PRReviewRequest) error         { return nil }
func (s *memSink) InsertPRLabel(model.PRLabel) error                         { return nil }
func (s *memSink) InsertRelease(model.Release) error                         { return nil }
func (s *memSink) InsertIncident(model.Incident) error                       { return nil }
func (s *memSink) InsertDefect(model.Defect) error                           { return nil }
func (s *memSink) InsertFileMetric(model.FileMetric) error                   { return nil }
func (s *memSink) InsertHarnessArtifact(model.HarnessArtifact) error        { return nil }
func (s *memSink) InsertFileComplexityHistory(model.FileComplexityHistory) error { return nil }
func (s *memSink) InsertRepoFile(model.RepoFile) error                       { return nil }

func TestPing_OK(t *testing.T) {
	client := vcr.NewGitHubClient(t, "testdata/cassettes/ping_ok")
	c := newForTest(client)

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: unexpected error: %v", err)
	}
}

func TestPing_401(t *testing.T) {
	client := vcr.NewGitHubClient(t, "testdata/cassettes/ping_401")
	c := newForTest(client)

	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping: expected error from 401 response, got nil")
	}
}

func TestBuilds_HappyPath(t *testing.T) {
	client := vcr.NewGitHubClient(t, "testdata/cassettes/builds_happy")
	c := newForTest(client)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), testRepo.Slug, testWindow)

	c.builds(context.Background(), "kmcd", "xray", testRepo, testWindow, sink, &prov)

	if len(prov.Errors) > 0 {
		t.Fatalf("builds errors: %v", prov.Errors)
	}
	if len(sink.builds) == 0 {
		t.Fatal("expected at least one build, got none")
	}
	for _, b := range sink.builds {
		if b.Repo != testRepo.Slug {
			t.Errorf("Build.Repo = %q, want %q", b.Repo, testRepo.Slug)
		}
		if b.Source != "github_actions" {
			t.Errorf("Build.Source = %q, want github_actions", b.Source)
		}
		if !testWindow.Contains(b.CreatedAt) {
			t.Errorf("Build.CreatedAt = %v outside window", b.CreatedAt)
		}
	}
	if prov.RowsReturned["builds"] != len(sink.builds) {
		t.Errorf("RowsReturned[builds]=%d, len(builds)=%d", prov.RowsReturned["builds"], len(sink.builds))
	}
}

func TestDeploys_Empty(t *testing.T) {
	client := vcr.NewGitHubClient(t, "testdata/cassettes/deploys_empty")
	c := newForTest(client)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), testRepo.Slug, testWindow)

	c.deploys(context.Background(), "kmcd", "xray", testRepo, testWindow, sink, &prov)

	if len(prov.Errors) > 0 {
		t.Fatalf("deploys errors: %v", prov.Errors)
	}
	if len(sink.deploys) != 0 {
		t.Errorf("expected zero deploys for kmcd/xray, got %d", len(sink.deploys))
	}
}

func TestExtract_HappyPath(t *testing.T) {
	client := vcr.NewGitHubClient(t, "testdata/cassettes/extract_happy")
	c := newForTest(client)
	sink := &memSink{}

	prov := c.Extract(context.Background(), testRepo, testWindow, sink)

	if len(prov.Errors) > 0 {
		t.Fatalf("Extract errors: %v", prov.Errors)
	}
	if len(sink.builds) == 0 {
		t.Fatal("expected at least one build, got none")
	}
	if prov.RowsReturned["builds"] != len(sink.builds) {
		t.Errorf("RowsReturned[builds]=%d, len(builds)=%d", prov.RowsReturned["builds"], len(sink.builds))
	}
}

func TestNew_MissingToken(t *testing.T) {
	_, err := New(config.GitHubActionsConn{Token: ""}, nil)
	if err == nil {
		t.Fatal("New: expected error for empty token")
	}
}

func TestNew_Success(t *testing.T) {
	c, err := New(config.GitHubActionsConn{Token: "test-token"}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Name() != "github_actions" {
		t.Errorf("Name() = %q, want github_actions", c.Name())
	}
}

func TestExtract_InvalidSlug(t *testing.T) {
	c := newForTest(&http.Client{})
	sink := &memSink{}

	prov := c.Extract(context.Background(), connector.Repo{Slug: "invalid-no-slash"}, testWindow, sink)

	if prov.Errors["repo"] == "" {
		t.Fatal("expected repo error for invalid slug, got none")
	}
}
