package github

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

func TestExtractTicketRefs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "single jira prefix",
			in:   "Fix PROJ-1 crash",
			want: []string{"PROJ-1"},
		},
		{
			name: "linear style",
			in:   "ENG-4567: tighten retry budget",
			want: []string{"ENG-4567"},
		},
		{
			name: "shortcut style",
			in:   "SC-89 done",
			want: []string{"SC-89"},
		},
		{
			name: "hash ref",
			in:   "closes #123",
			want: []string{"#123"},
		},
		{
			name: "hash ref at start of line",
			in:   "#42 first",
			want: []string{"#42"},
		},
		{
			name: "mixed",
			in:   "PROJ-1 and #7 and ENG-4567",
			want: []string{"PROJ-1", "ENG-4567", "#7"},
		},
		{
			name: "dedup repeated ref",
			in:   "PROJ-1 see PROJ-1 again",
			want: []string{"PROJ-1"},
		},
		{
			name: "non-match: lowercase prefix",
			in:   "Foo-12 not a ticket",
			want: nil,
		},
		{
			name: "non-match: single-char prefix",
			in:   "A-1 too short",
			want: nil,
		},
		{
			name: "non-match: leading digit prefix",
			in:   "1-foo not a ticket",
			want: nil,
		},
		{
			name: "non-match: hash attached to word",
			in:   "foo#123 inline",
			want: nil,
		},
		{
			name: "hash with leading punctuation",
			in:   "(#9) parenthesised",
			want: []string{"#9"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractTicketRefs(tc.in)

			// For "mixed" we care about set equality; first-seen order
			// is checked implicitly elsewhere. Sort both sides for
			// stable comparison except where the test explicitly asserts
			// preservation order.
			gotSorted := append([]string(nil), got...)
			wantSorted := append([]string(nil), tc.want...)
			sort.Strings(gotSorted)
			sort.Strings(wantSorted)
			if !reflect.DeepEqual(gotSorted, wantSorted) {
				t.Fatalf("extractTicketRefs(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractTicketRefsPreservesFirstSeenOrder(t *testing.T) {
	t.Parallel()
	got := extractTicketRefs("first ENG-1 then PROJ-2 then #3")
	want := []string{"ENG-1", "PROJ-2", "#3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %v, want %v", got, want)
	}
}

// defectSink is a minimal connector.Sink that records only the Defect rows
// inserted, for emitPRDefects assertions. Other Insert* methods return nil
// without recording. An optional defectErr lets tests exercise the error
// branch.
type defectSink struct {
	defects   []model.Defect
	defectErr error
}

func (s *defectSink) InsertRepo(model.Repo) error                       { return nil }
func (s *defectSink) InsertTeamRepo(string, string) error               { return nil }
func (s *defectSink) InsertRepoLanguage(model.RepoLanguage) error       { return nil }
func (s *defectSink) InsertBranch(model.Branch) error                   { return nil }
func (s *defectSink) InsertBranchProtection(model.BranchProtection) error {
	return nil
}
func (s *defectSink) InsertCodeowner(model.Codeowner) error             { return nil }
func (s *defectSink) InsertCommit(model.Commit) error                   { return nil }
func (s *defectSink) InsertCommitFile(model.CommitFile) error           { return nil }
func (s *defectSink) InsertCommitCoauthor(model.CommitCoauthor) error   { return nil }
func (s *defectSink) InsertPR(model.PR) error                           { return nil }
func (s *defectSink) InsertPRCommit(model.PRCommit) error               { return nil }
func (s *defectSink) InsertReview(model.Review) error                   { return nil }
func (s *defectSink) InsertPRComment(model.PRComment) error             { return nil }
func (s *defectSink) InsertPRReviewRequest(model.PRReviewRequest) error { return nil }
func (s *defectSink) InsertPRLabel(model.PRLabel) error                 { return nil }
func (s *defectSink) InsertBuild(model.Build) error                     { return nil }
func (s *defectSink) InsertBuildJob(model.BuildJob) error               { return nil }
func (s *defectSink) InsertDeploy(model.Deploy) error                   { return nil }
func (s *defectSink) InsertRelease(model.Release) error                 { return nil }
func (s *defectSink) InsertIncident(model.Incident) error               { return nil }
func (s *defectSink) InsertDefect(d model.Defect) error {
	if s.defectErr != nil {
		return s.defectErr
	}
	s.defects = append(s.defects, d)
	return nil
}
func (s *defectSink) InsertFileMetric(model.FileMetric) error           { return nil }
func (s *defectSink) InsertHarnessArtifact(model.HarnessArtifact) error { return nil }
func (s *defectSink) InsertFileComplexityHistory(model.FileComplexityHistory) error {
	return nil
}

func newDefectProv() *connector.Provenance {
	p := connector.NewProvenance("github", "kmcd/foo", connector.Window{})
	return &p
}

func TestEmitPRDefects_RefInTitleOnly(t *testing.T) {
	t.Parallel()
	sink := &defectSink{}
	prov := newDefectProv()
	openedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	emitPRDefects(sink, "kmcd/foo", 42, "Fix PROJ-1 crash", "no refs here", openedAt, nil, prov)

	if len(sink.defects) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(sink.defects), sink.defects)
	}
	got := sink.defects[0]
	if got.TicketRef != "PROJ-1" || got.Source != "pr_title" {
		t.Fatalf("got ref=%q source=%q, want PROJ-1/pr_title", got.TicketRef, got.Source)
	}
	if got.ID != "kmcd/foo:pr_title:42:PROJ-1" {
		t.Fatalf("got ID=%q, want kmcd/foo:pr_title:42:PROJ-1", got.ID)
	}
	if prov.RowsReturned["defects"] != 1 {
		t.Fatalf("prov rows=%d, want 1", prov.RowsReturned["defects"])
	}
}

func TestEmitPRDefects_RefInBodyOnly(t *testing.T) {
	t.Parallel()
	sink := &defectSink{}
	prov := newDefectProv()
	openedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	emitPRDefects(sink, "kmcd/foo", 42, "no refs here", "fixes PROJ-1", openedAt, nil, prov)

	if len(sink.defects) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(sink.defects), sink.defects)
	}
	got := sink.defects[0]
	if got.TicketRef != "PROJ-1" || got.Source != "pr_body" {
		t.Fatalf("got ref=%q source=%q, want PROJ-1/pr_body", got.TicketRef, got.Source)
	}
	if got.ID != "kmcd/foo:pr_body:42:PROJ-1" {
		t.Fatalf("got ID=%q, want kmcd/foo:pr_body:42:PROJ-1", got.ID)
	}
}

func TestEmitPRDefects_RefInBoth(t *testing.T) {
	t.Parallel()
	sink := &defectSink{}
	prov := newDefectProv()
	openedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	emitPRDefects(sink, "kmcd/foo", 42, "Fix PROJ-1 crash", "PROJ-1 details", openedAt, nil, prov)

	if len(sink.defects) != 1 {
		t.Fatalf("want 1 row (title wins per ADR 019), got %d: %+v", len(sink.defects), sink.defects)
	}
	got := sink.defects[0]
	if got.TicketRef != "PROJ-1" || got.Source != "pr_title" {
		t.Fatalf("got ref=%q source=%q, want PROJ-1/pr_title (title wins)", got.TicketRef, got.Source)
	}
}

func TestEmitPRDefects_DifferentRefsInEach(t *testing.T) {
	t.Parallel()
	sink := &defectSink{}
	prov := newDefectProv()
	openedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	emitPRDefects(sink, "kmcd/foo", 42, "PROJ-1: bug", "also closes ENG-2", openedAt, nil, prov)

	if len(sink.defects) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(sink.defects), sink.defects)
	}
	got := map[string]string{}
	for _, d := range sink.defects {
		got[d.TicketRef] = d.Source
	}
	want := map[string]string{"PROJ-1": "pr_title", "ENG-2": "pr_body"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if prov.RowsReturned["defects"] != 2 {
		t.Fatalf("prov rows=%d, want 2", prov.RowsReturned["defects"])
	}
}

func TestEmitPRDefects_EmptyInputs(t *testing.T) {
	t.Parallel()
	sink := &defectSink{}
	prov := newDefectProv()

	emitPRDefects(sink, "kmcd/foo", 42, "", "", time.Now(), nil, prov)

	if len(sink.defects) != 0 {
		t.Fatalf("want 0 rows, got %d: %+v", len(sink.defects), sink.defects)
	}
	if prov.RowsReturned["defects"] != 0 {
		t.Fatalf("prov rows=%d, want 0", prov.RowsReturned["defects"])
	}
}

func TestEmitPRDefects_SinkErrorRecordedOnce(t *testing.T) {
	t.Parallel()
	sink := &defectSink{defectErr: errors.New("boom")}
	prov := newDefectProv()

	emitPRDefects(sink, "kmcd/foo", 42, "PROJ-1", "ENG-2", time.Now(), nil, prov)

	if prov.Errors["defects"] != "boom" {
		t.Fatalf("prov errors=%q, want %q", prov.Errors["defects"], "boom")
	}
	if prov.RowsReturned["defects"] != 0 {
		t.Fatalf("prov rows=%d, want 0", prov.RowsReturned["defects"])
	}
}
