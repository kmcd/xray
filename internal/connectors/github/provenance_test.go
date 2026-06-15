package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// failingSink wraps extraSink and fails the Nth call to specific insert
// methods so tests can assert that per-row failures land in prov.Errors and
// don't abort the walk.
type failingSink struct {
	extraSink
	failOnFileMetric int
	fileMetricCalls  int
	failOnHarness    int
	harnessCalls     int
	failOnTeamRepo   int
	teamRepoCalls    int
}

func (s *failingSink) InsertFileMetric(fm model.FileMetric) error {
	s.fileMetricCalls++
	if s.failOnFileMetric != 0 && s.fileMetricCalls == s.failOnFileMetric {
		return errors.New("simulated file_metric insert failure")
	}
	return nil
}

func (s *failingSink) InsertHarnessArtifact(ha model.HarnessArtifact) error {
	s.harnessCalls++
	if s.failOnHarness != 0 && s.harnessCalls == s.failOnHarness {
		return errors.New("simulated harness insert failure")
	}
	return nil
}

func (s *failingSink) InsertTeamRepo(team, slug string) error {
	s.teamRepoCalls++
	if s.failOnTeamRepo != 0 && s.teamRepoCalls == s.failOnTeamRepo {
		return errors.New("simulated team_repo insert failure")
	}
	return nil
}

// TestExtractWorkingTree_FileMetricInsertError_RecordsProvErrors confirms that
// a sink failure on InsertFileMetric appends to prov.Errors and the walk
// continues past the failed row (Group A site walk.go:125).
func TestExtractWorkingTree_FileMetricInsertError_RecordsProvErrors(t *testing.T) {
	clone := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(clone, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &failingSink{failOnFileMetric: 2}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	if prov.Errors["file_metrics"] == "" {
		t.Errorf("expected prov.Errors[file_metrics] populated after failed insert; got empty")
	}
	if sink.fileMetricCalls < 3 {
		t.Errorf("walk aborted on failure: only %d InsertFileMetric calls; expected all 3 attempted", sink.fileMetricCalls)
	}
	if prov.RowsReturned["file_metrics"] != sink.fileMetricCalls-1 {
		t.Errorf("RowsReturned[file_metrics]=%d should equal successful inserts (%d)", prov.RowsReturned["file_metrics"], sink.fileMetricCalls-1)
	}
}

// TestExtractWorkingTree_HarnessArtifactInsertError_RecordsProvErrors covers
// walk.go:176 — InsertHarnessArtifact failure must land in prov.Errors and
// the walk must continue (Group A).
func TestExtractWorkingTree_HarnessArtifactInsertError_RecordsProvErrors(t *testing.T) {
	clone := t.TempDir()
	// Two harness files with unambiguous classification (CLAUDE.md, .cursorrules).
	if err := os.WriteFile(filepath.Join(clone, "CLAUDE.md"), []byte("instructions\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".cursorrules"), []byte("rules\n"), 0o644); err != nil {
		t.Fatalf("write .cursorrules: %v", err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &failingSink{failOnHarness: 1}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	if prov.Errors["harness_artifacts"] == "" {
		t.Errorf("expected prov.Errors[harness_artifacts] populated after failed insert; got empty")
	}
	if sink.harnessCalls < 2 {
		t.Errorf("walk aborted on failure: only %d InsertHarnessArtifact calls; expected 2", sink.harnessCalls)
	}
	// Pin RowsReturned[harness_artifacts] — kills `++ → --` mutation on
	// walk.go:185. With failOnHarness=1, exactly one successful insert.
	if got, want := prov.RowsReturned["harness_artifacts"], sink.harnessCalls-1; got != want {
		t.Errorf("RowsReturned[harness_artifacts] = %d, want %d (successful inserts)", got, want)
	}
}

// TestExtractWorkingTree_WalkRootError_RecordsRepoLanguagesError verifies that
// a root-directory walk failure sets prov.Errors["repo_languages"] in addition
// to prov.Errors["walk"], so analysts inspecting the manifest can see why
// repo_languages is absent rather than finding an empty error key.
func TestExtractWorkingTree_WalkRootError_RecordsRepoLanguagesError(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	// Use a path guaranteed absent: a child of a fresh temp dir that was never created.
	absentClone := filepath.Join(t.TempDir(), "does-not-exist")
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: absentClone}, standardWindow(), sink, &prov)

	for _, key := range []string{"walk", "file_metrics", "harness_artifacts", "repo_languages"} {
		if prov.Errors[key] == "" {
			t.Errorf("expected prov.Errors[%q] set on root walk failure; got empty", key)
		}
	}
}

// TestExtractWorkingTree_RepoLanguages_ByteCounts verifies that the per-language
// byte totals accumulated in the walk equal the sum of on-disk file sizes for
// each language. Replaces the deleted TestExtractLanguages assertion that was
// removed in #105.
func TestExtractWorkingTree_RepoLanguages_ByteCounts(t *testing.T) {
	goContent1 := "package main\n\nfunc main() {}\n"
	goContent2 := "package main\n\nfunc h() {}\n"
	rbContent := "puts 'hi'\n"

	clone := t.TempDir()
	for name, content := range map[string]string{
		"main.go":   goContent1,
		"helper.go": goContent2,
		"app.rb":    rbContent,
	} {
		if err := os.WriteFile(filepath.Join(clone, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	byLang := make(map[string]int64)
	for _, row := range sink.languages {
		byLang[row.Language] = row.Bytes
	}

	wantGo := int64(len(goContent1) + len(goContent2))
	if byLang["Go"] != wantGo {
		t.Errorf("Go bytes = %d, want %d (sum of .go file sizes)", byLang["Go"], wantGo)
	}
	wantRuby := int64(len(rbContent))
	if byLang["Ruby"] != wantRuby {
		t.Errorf("Ruby bytes = %d, want %d", byLang["Ruby"], wantRuby)
	}
}

// TestExtractWorkingTree_RepoLanguages_RowsReturned pins the language-totals
// emission at walk.go:199 — kills `++ → --` and other mutations on that
// increment. Two Go files + one Python file → 2 language rows + at least
// one repo_languages row per distinct language.
func TestExtractWorkingTree_RepoLanguages_RowsReturned(t *testing.T) {
	clone := t.TempDir()
	if err := os.WriteFile(filepath.Join(clone, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, "b.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(clone, "c.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatalf("write c.py: %v", err)
	}

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractWorkingTree(context.Background(), connector.Repo{Slug: "kmcd/foo", Clone: clone}, standardWindow(), sink, &prov)

	if prov.RowsReturned["repo_languages"] == 0 {
		t.Errorf("RowsReturned[repo_languages] = 0; expected > 0 (mutation `++ → --` would also yield 0 or negative)")
	}
	if got := prov.RowsReturned["repo_languages"]; got != len(sink.languages) {
		t.Errorf("RowsReturned[repo_languages] = %d, sink got %d", got, len(sink.languages))
	}
}

// TestInsertTeamMapping_Success_IncrementsRowsReturned covers extract.go:84
// Group B — successful InsertTeamRepo bumps RowsReturned[teams].
func TestInsertTeamMapping_Success_IncrementsRowsReturned(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &failingSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	c.insertTeamMapping(connector.Repo{Slug: "kmcd/foo", Team: "platform"}, sink, &prov)

	if prov.RowsReturned["teams"] != 1 {
		t.Errorf("expected RowsReturned[teams]=1 after success; got %d", prov.RowsReturned["teams"])
	}
	if prov.Errors["teams"] != "" {
		t.Errorf("expected no error on success; got %q", prov.Errors["teams"])
	}
}

// TestInsertTeamMapping_Failure_RecordsErrorNoIncrement covers Group B's
// negative path — the increment must NOT fire when InsertTeamRepo fails.
func TestInsertTeamMapping_Failure_RecordsErrorNoIncrement(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &failingSink{failOnTeamRepo: 1}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	c.insertTeamMapping(connector.Repo{Slug: "kmcd/foo", Team: "platform"}, sink, &prov)

	if prov.RowsReturned["teams"] != 0 {
		t.Errorf("expected RowsReturned[teams]=0 after failure; got %d", prov.RowsReturned["teams"])
	}
	if prov.Errors["teams"] == "" {
		t.Errorf("expected prov.Errors[teams] populated after failure; got empty")
	}
}

// TestInsertTeamMapping_NoTeam_NoEmission covers the no-op path: an empty
// repo.Team must not call InsertTeamRepo at all.
func TestInsertTeamMapping_NoTeam_NoEmission(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &failingSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	c.insertTeamMapping(connector.Repo{Slug: "kmcd/foo"}, sink, &prov)

	if sink.teamRepoCalls != 0 {
		t.Errorf("expected no InsertTeamRepo call for empty team; got %d", sink.teamRepoCalls)
	}
}

// TestPaginatePRCommits_QueryError_RecordsProvErrors covers Group A site
// prs.go:662 — a queryWithEOFRetry failure must populate the per-PR
// prov.Errors[fmt.Sprintf("pr_commits:%d", number)] key AND set
// PaginationComplete=false (preserving the existing flip on this path).
func TestPaginatePRCommits_QueryError_RecordsProvErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &memSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())

	b := openPRCommitsBatch(sink)
	oids := c.paginatePRCommits(context.Background(), "kmcd", "foo", 77, "kmcd/foo", "cursor-start", b, &prov)
	commitBatch(b, &prov, "pr_commits")

	if len(oids) != 0 {
		t.Errorf("expected no oids on query error; got %v", oids)
	}
	if prov.PaginationComplete {
		t.Errorf("expected PaginationComplete=false after query error; got true")
	}
	wantKey := fmt.Sprintf("pr_commits:%d", 77)
	if got := prov.Errors[wantKey]; got == "" {
		t.Errorf("expected prov.Errors[%q] populated after query error; got empty", wantKey)
	}
}

// TestExtractReleases_MidWalkError_FlipsPaginationComplete covers Group C
// site releases.go:35 — when ListReleases errors on a non-first page, the
// connector must flip PaginationComplete=false alongside the existing
// prov.Errors / EndpointStatus writes.
func TestExtractReleases_MidWalkError_FlipsPaginationComplete(t *testing.T) {
	mux := http.NewServeMux()
	var pageHits int
	mux.HandleFunc("/repos/kmcd/foo/releases", func(w http.ResponseWriter, r *http.Request) {
		pageHits++
		if pageHits == 1 {
			// First page: return one in-window release with Link: next.
			w.Header().Set("Link", `<`+r.URL.Path+`?page=2>; rel="next"`)
			payload := []map[string]any{
				{"tag_name": "v1.0.0", "name": "ok", "created_at": "2025-06-15T00:00:00Z", "target_commitish": "main"},
			}
			b, _ := json.Marshal(payload)
			_, _ = w.Write(b)
			return
		}
		// Subsequent pages: 500.
		http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/v1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1111111111111111111111111111111111111111"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if prov.PaginationComplete {
		t.Errorf("expected PaginationComplete=false after mid-walk error; got true")
	}
	if prov.Errors["releases"] == "" {
		t.Errorf("expected prov.Errors[releases] populated after mid-walk error; got empty")
	}
}

// TestExtractReleases_InvalidSlug_RecordsInaccessible covers the early-return
// path on a malformed slug. The endpoint was never tried; recording
// Accessible:false with a clear Reason lets the analyser distinguish a config
// error from an empty-but-reachable endpoint.
func TestExtractReleases_InvalidSlug_RecordsInaccessible(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "malformed", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "malformed"}, standardWindow(), sink, &prov)

	ep, ok := prov.Endpoints["releases"]
	if !ok {
		t.Fatalf("expected endpoints[releases] entry on invalid slug")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on invalid slug; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on invalid slug; got empty")
	}
}

// TestInsertRepoRow_InvalidSlug_RecordsInaccessible mirrors the above for the
// insertRepoRow path. Both repo_metadata and contributors must record
// Accessible:false; the repos row is still emitted (the slug is what we know).
func TestInsertRepoRow_InvalidSlug_RecordsInaccessible(t *testing.T) {
	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "malformed", standardWindow())
	if err := c.insertRepoRow(context.Background(), connector.Repo{Slug: "malformed"}, standardWindow(), sink, &prov); err != nil {
		t.Fatalf("insertRepoRow: %v", err)
	}
	for _, key := range []string{"repo_metadata", "contributors"} {
		ep, ok := prov.Endpoints[key]
		if !ok {
			t.Errorf("expected endpoints[%s] entry on invalid slug", key)
			continue
		}
		if ep.Accessible {
			t.Errorf("expected endpoints[%s].Accessible=false on invalid slug; got %+v", key, ep)
		}
		if ep.Reason == "" {
			t.Errorf("expected endpoints[%s].Reason populated; got empty", key)
		}
	}
	if prov.RowsReturned["repos"] != 1 {
		t.Errorf("expected repos row still emitted on invalid slug; got RowsReturned=%d", prov.RowsReturned["repos"])
	}
}

// TestExtractBranches_InvalidSlug_RecordsBranchProtectionInaccessible covers
// the early-return path in extractBranches. branch_protection is the only
// GitHub-endpoint key extractBranches owns (branches itself is git-clone-derived
// and falls outside the EndpointStatus contract).
func TestExtractBranches_InvalidSlug_RecordsBranchProtectionInaccessible(t *testing.T) {
	clone := setupCloneWithRemoteRefs(t, []string{"main"})

	c := newTestConnector(t, httptest.NewServer(http.NewServeMux()))
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "malformed", standardWindow())
	c.extractBranches(context.Background(), connector.Repo{Slug: "malformed", Clone: clone}, sink, &prov)

	ep, ok := prov.Endpoints["branch_protection"]
	if !ok {
		t.Fatalf("expected endpoints[branch_protection] entry on invalid slug")
	}
	if ep.Accessible {
		t.Errorf("expected Accessible=false on invalid slug; got %+v", ep)
	}
	if ep.Reason == "" {
		t.Errorf("expected Reason populated on invalid slug; got empty")
	}
}

// TestExtractPRs_PrefetchError_RecordsProvErrors covers the prefetch
// failure-recording path in extract_prs.go's switch — when consumePRPrefetch
// returns err != nil with a resume cursor, the err is captured in
// prov.Errors[prs:prefetch] before the live resume attempts to clear it.
func TestExtractPRs_GraphQLError_FlipsPaginationComplete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"server error"}`, http.StatusInternalServerError)
	})
	mux.HandleFunc("/repos/kmcd/foo/contents/.github/PULL_REQUEST_TEMPLATE.md", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestConnector(t, srv)
	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractPRs(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if prov.PaginationComplete {
		t.Errorf("expected PaginationComplete=false after GraphQL error; got true")
	}
	if prov.Errors["prs"] == "" {
		t.Errorf("expected prov.Errors[prs] populated after error; got empty")
	}
}
