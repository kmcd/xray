package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
)

// buildSearchResponse builds a GraphQL response for the prSearchQuery shape.
func buildSearchResponse(issueCount int, nodes []prNodeJSON, hasNextPage bool, endCursor string) string {
	type searchPayload struct {
		IssueCount int           `json:"issueCount"`
		PageInfo   pageInfoNode  `json:"pageInfo"`
		Nodes      []prNodeJSON  `json:"nodes"`
	}
	payload := map[string]any{
		"data": map[string]any{
			"search": searchPayload{
				IssueCount: issueCount,
				PageInfo:   pageInfoNode{EndCursor: endCursor, HasNextPage: hasNextPage},
				Nodes:      nodes,
			},
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func makePRNode(num int, createdAt string) prNodeJSON {
	return prNodeJSON{
		Number:       num,
		Title:        fmt.Sprintf("PR %d", num),
		Body:         "",
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
		Author:       loginNode{Login: "user"},
		HeadRefOid:   fmt.Sprintf("sha%d", num),
		HeadRepository: repoNameNode{NameWithOwner: "owner/repo"},
		MergeCommit:  nil,
		Commits:      commitConn{TotalCount: 1, Nodes: []commitConnNodeWrap{{Commit: oidNode{Oid: fmt.Sprintf("sha%d", num)}}}},
	}
}

// TestMonthBuckets verifies correct UTC calendar-month bucket generation.
func TestMonthBuckets(t *testing.T) {
	slice := connector.Window{
		Start: time.Date(2022, 1, 15, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 3, 10, 0, 0, 0, 0, time.UTC),
	}
	got := monthBuckets(slice)
	if len(got) != 3 {
		t.Fatalf("expected 3 buckets, got %d: %+v", len(got), got)
	}
	// First bucket clipped to slice.Start.
	if !got[0].Start.Equal(slice.Start) {
		t.Errorf("bucket[0].Start = %s, want %s", got[0].Start, slice.Start)
	}
	// February starts on 2022-02-01.
	wantFebStart := time.Date(2022, 2, 1, 0, 0, 0, 0, time.UTC)
	if !got[1].Start.Equal(wantFebStart) {
		t.Errorf("bucket[1].Start = %s, want %s", got[1].Start, wantFebStart)
	}
	// Last bucket clipped to slice.End.
	if !got[2].End.Equal(slice.End) {
		t.Errorf("bucket[2].End = %s, want %s", got[2].End, slice.End)
	}
	// Labels
	wantLabels := []string{"2022-01", "2022-02", "2022-03"}
	for i, b := range got {
		if b.Label != wantLabels[i] {
			t.Errorf("bucket[%d].Label = %q, want %q", i, b.Label, wantLabels[i])
		}
	}
}

// TestMonthBuckets_SingleMonth verifies a slice contained within one month
// produces exactly one bucket.
func TestMonthBuckets_SingleMonth(t *testing.T) {
	slice := connector.Window{
		Start: time.Date(2022, 6, 5, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 6, 20, 0, 0, 0, 0, time.UTC),
	}
	got := monthBuckets(slice)
	if len(got) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(got))
	}
	if !got[0].Start.Equal(slice.Start) || !got[0].End.Equal(slice.End) {
		t.Errorf("single-month bucket = {%s..%s}, want {%s..%s}",
			got[0].Start, got[0].End, slice.Start, slice.End)
	}
}

// TestWeeklyBucketsFor verifies that a month is split into contiguous 7-day
// sub-buckets with the correct labels and no overlap.
func TestWeeklyBucketsFor(t *testing.T) {
	m := monthBucket{
		Label: "2022-01",
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 1, 31, 23, 59, 59, 0, time.UTC),
	}
	weeks := weeklyBucketsFor(m)
	if len(weeks) == 0 {
		t.Fatal("expected at least one weekly bucket")
	}
	// First week starts at month start.
	if !weeks[0].Start.Equal(m.Start) {
		t.Errorf("week[0].Start = %s, want %s", weeks[0].Start, m.Start)
	}
	// Last week ends at month end.
	if !weeks[len(weeks)-1].End.Equal(m.End) {
		t.Errorf("week[last].End = %s, want %s", weeks[len(weeks)-1].End, m.End)
	}
	// Labels are "2022-01-W1", "2022-01-W2", etc.
	for i, w := range weeks {
		wantLabel := fmt.Sprintf("2022-01-W%d", i+1)
		if w.Label != wantLabel {
			t.Errorf("week[%d].Label = %q, want %q", i, w.Label, wantLabel)
		}
	}
	// No overlap: week[i].End + 1s == week[i+1].Start.
	for i := 1; i < len(weeks); i++ {
		expected := weeks[i-1].End.Add(time.Second)
		if !weeks[i].Start.Equal(expected) {
			t.Errorf("week[%d].Start = %s, want %s (no overlap with week[%d])",
				i, weeks[i].Start, expected, i-1)
		}
	}
}

// TestBucketSeed_Deterministic verifies that the same (slug, label) always
// produces the same seed and that different inputs produce different seeds.
func TestBucketSeed_Deterministic(t *testing.T) {
	s1 := bucketSeed("owner/repo", "2022-01")
	s2 := bucketSeed("owner/repo", "2022-01")
	if s1 != s2 {
		t.Errorf("bucketSeed not deterministic: %d vs %d", s1, s2)
	}
	s3 := bucketSeed("owner/repo", "2022-02")
	if s1 == s3 {
		t.Error("different labels produced the same seed")
	}
	s4 := bucketSeed("owner/other", "2022-01")
	if s1 == s4 {
		t.Error("different slugs produced the same seed")
	}
}

// TestRandomPickN verifies deterministic output for the same seed and correct
// truncation to N.
func TestRandomPickN_Deterministic(t *testing.T) {
	nodes := make([]prGraph, 10)
	for i := range nodes {
		nodes[i].Number = githubv4.Int(i + 1)
	}

	got1 := randomPickN(nodes, 4, 42)
	got2 := randomPickN(nodes, 4, 42)
	if len(got1) != 4 || len(got2) != 4 {
		t.Fatalf("expected 4 nodes, got %d and %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i].Number != got2[i].Number {
			t.Errorf("index %d: got1=%v got2=%v — not deterministic", i, got1[i].Number, got2[i].Number)
		}
	}
	// Different seed → different order (probabilistically).
	got3 := randomPickN(nodes, 4, 99)
	same := true
	for i := range got1 {
		if got1[i].Number != got3[i].Number {
			same = false
			break
		}
	}
	if same {
		t.Error("different seeds produced identical output — suspicious")
	}
}

// TestRandomPickN_NoTruncation verifies that fewer than N nodes are returned
// unchanged.
func TestRandomPickN_NoTruncation(t *testing.T) {
	nodes := make([]prGraph, 3)
	for i := range nodes {
		nodes[i].Number = githubv4.Int(i + 1)
	}
	got := randomPickN(nodes, 10, 42)
	if len(got) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(got))
	}
}

// TestSearchPRsInRange_HappyPath verifies that searchPRsInRange returns the
// correct number of nodes and the totalCount from a single-page response.
func TestSearchPRsInRange_HappyPath(t *testing.T) {
	nodes := []prNodeJSON{
		makePRNode(1, "2022-01-10T00:00:00Z"),
		makePRNode(2, "2022-01-15T00:00:00Z"),
		makePRNode(3, "2022-01-20T00:00:00Z"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildSearchResponse(3, nodes, false, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)

	start := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2022, 1, 31, 23, 59, 59, 0, time.UTC)
	got, total, err := c.searchPRsInRange(context.Background(), "owner/repo", start, end, 20)
	if err != nil {
		t.Fatalf("searchPRsInRange: %v", err)
	}
	if total != 3 {
		t.Errorf("totalCount = %d, want 3", total)
	}
	if len(got) != 3 {
		t.Errorf("nodes returned = %d, want 3", len(got))
	}
}

// TestFetchBucket_HappyPath verifies fetchBucket records correct
// Target/Actual/Total in the SampleBucket metadata.
func TestFetchBucket_HappyPath(t *testing.T) {
	nodes := []prNodeJSON{
		makePRNode(1, "2022-01-10T00:00:00Z"),
		makePRNode(2, "2022-01-15T00:00:00Z"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildSearchResponse(2, nodes, false, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)

	b := monthBucket{
		Label: "2022-01",
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 1, 31, 23, 59, 59, 0, time.UTC),
	}
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 20}
	results := c.fetchBucket(context.Background(), "owner/repo", b, spec)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	meta := results[0].meta
	if meta.Target != 20 {
		t.Errorf("Target = %d, want 20", meta.Target)
	}
	if meta.Actual != 2 {
		t.Errorf("Actual = %d, want 2", meta.Actual)
	}
	if meta.Total != 2 {
		t.Errorf("Total = %d, want 2", meta.Total)
	}
	if meta.Truncated {
		t.Error("Truncated should be false")
	}
}

// TestFetchBucket_TruncationSplitsToWeekly verifies that when totalCount >
// 1000 the parent bucket is marked truncated and weekly sub-buckets are
// returned.
func TestFetchBucket_TruncationSplitsToWeekly(t *testing.T) {
	var callCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		// First call (month query): report 1500 total (triggers truncation).
		// Subsequent calls (weekly sub-bucket queries): report 200 each.
		total := 200
		if callCount == 1 {
			total = 1500
		}
		fmt.Fprint(w, buildSearchResponse(total, []prNodeJSON{makePRNode(callCount, "2022-01-10T00:00:00Z")}, false, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)

	b := monthBucket{
		Label: "2022-01",
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 1, 31, 23, 59, 59, 0, time.UTC),
	}
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 20}
	results := c.fetchBucket(context.Background(), "owner/repo", b, spec)

	// First result: parent bucket with Truncated=true and no nodes.
	if len(results) < 2 {
		t.Fatalf("expected multiple results (parent + weekly), got %d", len(results))
	}
	parent := results[0]
	if !parent.meta.Truncated {
		t.Error("parent bucket Truncated should be true")
	}
	if parent.meta.Month != "2022-01" {
		t.Errorf("parent Month = %q, want 2022-01", parent.meta.Month)
	}
	if len(parent.nodes) != 0 {
		t.Errorf("parent nodes = %d, want 0", len(parent.nodes))
	}
	// Weekly sub-buckets have labels like "2022-01-W1".
	for _, r := range results[1:] {
		if !strings.HasPrefix(r.meta.Month, "2022-01-W") {
			t.Errorf("weekly sub-bucket Month = %q, want 2022-01-Wn prefix", r.meta.Month)
		}
	}
}

// TestExtractSparsePRs_EndToEnd verifies that extractSparsePRs emits PR rows
// and records per-bucket metadata in prov.Sampling.Buckets.
func TestExtractSparsePRs_EndToEnd(t *testing.T) {
	// Two-month slice: 2022-01 and 2022-02, 3 PRs per month.
	jan := []prNodeJSON{
		makePRNode(1, "2022-01-05T00:00:00Z"),
		makePRNode(2, "2022-01-10T00:00:00Z"),
		makePRNode(3, "2022-01-20T00:00:00Z"),
	}
	feb := []prNodeJSON{
		makePRNode(4, "2022-02-05T00:00:00Z"),
		makePRNode(5, "2022-02-10T00:00:00Z"),
		makePRNode(6, "2022-02-20T00:00:00Z"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		// Route by which month appears in the query string.
		if strings.Contains(string(body), "2022-02") {
			fmt.Fprint(w, buildSearchResponse(3, feb, false, ""))
		} else {
			fmt.Fprint(w, buildSearchResponse(3, jan, false, ""))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)

	slice := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 2, 28, 23, 59, 59, 0, time.UTC),
	}
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 20}
	sink := &memSink{}
	prov := connector.NewProvenance("github", "owner/repo", slice)
	prov.Sampling = &connector.SamplingProvenance{Strategy: "search_default_relevance"}

	c.extractSparsePRs(context.Background(), connector.Repo{Slug: "owner/repo"}, slice, spec, sink, &prov)

	if len(sink.prs) != 6 {
		t.Errorf("emitted %d PR rows, want 6", len(sink.prs))
	}
	if prov.Sampling == nil {
		t.Fatal("prov.Sampling is nil")
	}
	if len(prov.Sampling.Buckets) != 2 {
		t.Errorf("Sampling.Buckets has %d entries, want 2", len(prov.Sampling.Buckets))
	}
	// Buckets are sorted by month label.
	if len(prov.Sampling.Buckets) >= 1 && prov.Sampling.Buckets[0].Month != "2022-01" {
		t.Errorf("Buckets[0].Month = %q, want 2022-01", prov.Sampling.Buckets[0].Month)
	}
	if len(prov.Sampling.Buckets) >= 2 && prov.Sampling.Buckets[1].Month != "2022-02" {
		t.Errorf("Buckets[1].Month = %q, want 2022-02", prov.Sampling.Buckets[1].Month)
	}
}

// TestExtractSparsePRs_Race verifies that concurrent bucket fetches and
// serial aggregation produce no data races. Run with -race.
func TestExtractSparsePRs_Race(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		nodes := []prNodeJSON{makePRNode(1, "2022-01-10T00:00:00Z")}
		fmt.Fprint(w, buildSearchResponse(1, nodes, false, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newTestConnector(t, srv)

	// 8 months to exercise concurrent fetching (cap is 4).
	slice := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 8, 31, 23, 59, 59, 0, time.UTC),
	}
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 5}
	sink := &memSink{}
	prov := connector.NewProvenance("github", "owner/repo", slice)
	prov.Sampling = &connector.SamplingProvenance{Strategy: "search_default_relevance"}

	c.extractSparsePRs(context.Background(), connector.Repo{Slug: "owner/repo"}, slice, spec, sink, &prov)

	// 8 months × 1 PR each = 8 rows.
	if len(sink.prs) != 8 {
		t.Errorf("emitted %d PR rows, want 8", len(sink.prs))
	}
}

// TestExtractSparsePRs_EmptySlice verifies that a zero-width slice produces
// no buckets and no rows without panicking.
func TestExtractSparsePRs_EmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()
	c := newTestConnector(t, srv)

	now := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
	// End before Start: no buckets.
	slice := connector.Window{Start: now, End: now.Add(-time.Hour)}
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 20}
	sink := &memSink{}
	prov := connector.NewProvenance("github", "owner/repo", slice)
	prov.Sampling = &connector.SamplingProvenance{}

	c.extractSparsePRs(context.Background(), connector.Repo{Slug: "owner/repo"}, slice, spec, sink, &prov)

	if len(sink.prs) != 0 {
		t.Errorf("expected 0 PRs for empty slice, got %d", len(sink.prs))
	}
}

// TestRandomPickN_Consistency verifies that calling randomPickN with the same
// seed on the same inputs produces identical picks across multiple calls —
// simulating quarterly re-extraction stability.
func TestRandomPickN_Consistency(t *testing.T) {
	nodes := make([]prGraph, 50)
	for i := range nodes {
		nodes[i].Number = githubv4.Int(i + 1)
	}
	seed := bucketSeed("org/repo", "2022-06")
	run1 := randomPickN(nodes, 10, seed)
	run2 := randomPickN(nodes, 10, seed)
	for i := range run1 {
		if run1[i].Number != run2[i].Number {
			t.Errorf("index %d: run1=%v run2=%v — not stable across re-runs", i, run1[i].Number, run2[i].Number)
		}
	}
}

