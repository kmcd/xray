package circleci

import (
	"testing"
	"time"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
)

func TestMonthBuckets(t *testing.T) {
	slice := connector.Window{
		Start: time.Date(2022, 1, 15, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 3, 10, 0, 0, 0, 0, time.UTC),
	}
	got := monthBuckets(slice)
	if len(got) != 3 {
		t.Fatalf("expected 3 buckets, got %d: %+v", len(got), got)
	}
	if !got[0].Start.Equal(slice.Start) {
		t.Errorf("bucket[0].Start = %s, want %s", got[0].Start, slice.Start)
	}
	wantFebStart := time.Date(2022, 2, 1, 0, 0, 0, 0, time.UTC)
	if !got[1].Start.Equal(wantFebStart) {
		t.Errorf("bucket[1].Start = %s, want %s", got[1].Start, wantFebStart)
	}
	if !got[2].End.Equal(slice.End) {
		t.Errorf("bucket[2].End = %s, want %s", got[2].End, slice.End)
	}
	wantLabels := []string{"2022-01", "2022-02", "2022-03"}
	for i, b := range got {
		if b.Label != wantLabels[i] {
			t.Errorf("bucket[%d].Label = %q, want %q", i, b.Label, wantLabels[i])
		}
	}
}

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

func makePipelines(n int, base time.Time) []pipeline {
	ps := make([]pipeline, n)
	for i := range ps {
		ps[i] = pipeline{
			ID:        string(rune('a' + i)),
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}
	}
	return ps
}

func TestSamplePipelines_Deterministic(t *testing.T) {
	ps := makePipelines(10, time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC))
	got1 := samplePipelines(ps, 4, 42)
	got2 := samplePipelines(ps, 4, 42)
	if len(got1) != 4 || len(got2) != 4 {
		t.Fatalf("expected 4 pipelines, got %d and %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i].ID != got2[i].ID {
			t.Errorf("index %d: got1=%q got2=%q — not deterministic", i, got1[i].ID, got2[i].ID)
		}
	}
	got3 := samplePipelines(ps, 4, 99)
	same := true
	for i := range got1 {
		if got1[i].ID != got3[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Error("different seeds produced identical output — suspicious")
	}
}

func TestSamplePipelines_NoTruncation(t *testing.T) {
	ps := makePipelines(3, time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC))
	got := samplePipelines(ps, 10, 42)
	if len(got) != 3 {
		t.Errorf("expected 3 pipelines, got %d", len(got))
	}
}

func TestSamplePipelines_Consistency(t *testing.T) {
	ps := makePipelines(50, time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC))
	seed := bucketSeed("org/repo", "2022-06")
	run1 := samplePipelines(ps, 10, seed)
	run2 := samplePipelines(ps, 10, seed)
	for i := range run1 {
		if run1[i].ID != run2[i].ID {
			t.Errorf("index %d: run1=%q run2=%q — not stable across re-runs", i, run1[i].ID, run2[i].ID)
		}
	}
}

func TestSelectPipelines_NonSparseIdentity(t *testing.T) {
	c := &Connector{} // bracketStart nil → non-sparse
	ps := makePipelines(5, time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC))
	prov := connector.NewProvenance("circleci", "a/b", connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 6, 30, 0, 0, 0, 0, time.UTC),
	})
	got := c.selectPipelines(ps, "a/b", connector.Window{}, &prov)
	if len(got) != len(ps) {
		t.Errorf("non-sparse: expected %d pipelines, got %d", len(ps), len(got))
	}
	if prov.Sampling != nil {
		t.Error("non-sparse: Sampling should remain nil")
	}
}

// TestSelectPipelines_BracketWithoutSample covers the case where bracketStart
// is set but sampleSpec is nil. selectPipelines must return all pipelines
// unchanged and must NOT write to prov.Sampling (full-fidelity run).
func TestSelectPipelines_BracketWithoutSample(t *testing.T) {
	inflection := time.Date(2022, 4, 1, 0, 0, 0, 0, time.UTC)
	bw := &config.DurationSpec{Months: 1, Raw: "1m"}
	bracketStart := inflection.AddDate(0, -1, 0) // 2022-03-01
	c := &Connector{
		bracketStart: &bracketStart,
		bracketSpec:  bw,
		inflection:   &inflection,
		sampleSpec:   nil, // no sampling configured
	}
	ps := makePipelines(5, time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC))
	window := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 4, 30, 0, 0, 0, 0, time.UTC),
	}
	prov := connector.NewProvenance("circleci", "a/b", window)
	got := c.selectPipelines(ps, "a/b", window, &prov)
	if len(got) != len(ps) {
		t.Errorf("bracket-without-sample: expected all %d pipelines, got %d", len(ps), len(got))
	}
	if prov.Sampling != nil {
		t.Error("bracket-without-sample: Sampling should remain nil for full-fidelity run")
	}
}

func TestSelectPipelines_Partition(t *testing.T) {
	inflection := time.Date(2022, 4, 1, 0, 0, 0, 0, time.UTC)
	bw := &config.DurationSpec{Months: 1, Raw: "1m"}
	bracketStart := inflection.AddDate(0, -1, 0) // 2022-03-01
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 2, Random: false, Raw: "monthly:2"}

	c := &Connector{
		bracketStart: &bracketStart,
		bracketSpec:  bw,
		inflection:   &inflection,
		sampleSpec:   spec,
	}

	window := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 4, 30, 0, 0, 0, 0, time.UTC),
	}

	// Build pipelines: 3 in Jan, 3 in Feb, 4 in Mar+ (full-fidelity).
	base := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	ps := []pipeline{
		{ID: "jan1", CreatedAt: base.Add(1 * 24 * time.Hour)},
		{ID: "jan2", CreatedAt: base.Add(5 * 24 * time.Hour)},
		{ID: "jan3", CreatedAt: base.Add(10 * 24 * time.Hour)},
		{ID: "feb1", CreatedAt: time.Date(2022, 2, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "feb2", CreatedAt: time.Date(2022, 2, 10, 0, 0, 0, 0, time.UTC)},
		{ID: "feb3", CreatedAt: time.Date(2022, 2, 20, 0, 0, 0, 0, time.UTC)},
		// >= bracketStart (2022-03-01): full fidelity
		{ID: "mar1", CreatedAt: time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "mar2", CreatedAt: time.Date(2022, 3, 15, 0, 0, 0, 0, time.UTC)},
		{ID: "apr1", CreatedAt: time.Date(2022, 4, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "apr2", CreatedAt: time.Date(2022, 4, 20, 0, 0, 0, 0, time.UTC)},
	}

	prov := connector.NewProvenance("circleci", "a/b", window)
	prov.Sampling = &connector.SamplingProvenance{}

	got := c.selectPipelines(ps, "a/b", window, &prov)

	// Full-fidelity set (4 pipelines): all returned.
	fullFidelityIDs := map[string]bool{"mar1": true, "mar2": true, "apr1": true, "apr2": true}
	for _, p := range got {
		if fullFidelityIDs[p.ID] {
			delete(fullFidelityIDs, p.ID)
		}
	}
	if len(fullFidelityIDs) != 0 {
		t.Errorf("missing full-fidelity pipelines: %v", fullFidelityIDs)
	}

	// Pre-bracket: Jan has 3, sample N=2 → 2; Feb has 3, sample N=2 → 2.
	total := len(got)
	wantTotal := 4 + 2 + 2
	if total != wantTotal {
		t.Errorf("total pipelines = %d, want %d", total, wantTotal)
	}

	// Provenance: 2 buckets (Jan, Feb), correct counts.
	if prov.Sampling == nil {
		t.Fatal("Sampling is nil")
	}
	buckets := prov.Sampling.Buckets
	if len(buckets) != 2 {
		t.Fatalf("expected 2 sampling buckets, got %d: %v", len(buckets), buckets)
	}
	for _, b := range buckets {
		if b.Total != 3 {
			t.Errorf("bucket %s: Total = %d, want 3", b.Month, b.Total)
		}
		if b.Actual != 2 {
			t.Errorf("bucket %s: Actual = %d, want 2", b.Month, b.Actual)
		}
		if b.Target != 2 {
			t.Errorf("bucket %s: Target = %d, want 2", b.Month, b.Target)
		}
	}
}

func TestSelectPipelines_EmptyMonthRecorded(t *testing.T) {
	inflection := time.Date(2022, 4, 1, 0, 0, 0, 0, time.UTC)
	bw := &config.DurationSpec{Months: 3, Raw: "3m"}
	bracketStart := inflection.AddDate(0, -3, 0) // 2022-01-01
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 5, Random: false, Raw: "monthly:5"}

	c := &Connector{
		bracketStart: &bracketStart,
		bracketSpec:  bw,
		inflection:   &inflection,
		sampleSpec:   spec,
	}

	window := connector.Window{
		Start: time.Date(2021, 10, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 4, 30, 0, 0, 0, 0, time.UTC),
	}

	// Only Nov has pipelines in the pre-bracket slice (Oct, Nov, Dec are pre-bracket).
	ps := []pipeline{
		{ID: "nov1", CreatedAt: time.Date(2021, 11, 10, 0, 0, 0, 0, time.UTC)},
		// Jan+ is full fidelity
		{ID: "jan1", CreatedAt: time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)},
	}

	prov := connector.NewProvenance("circleci", "a/b", window)
	prov.Sampling = &connector.SamplingProvenance{}

	c.selectPipelines(ps, "a/b", window, &prov)

	// 3 pre-bracket months: Oct, Nov, Dec.
	buckets := prov.Sampling.Buckets
	if len(buckets) != 3 {
		t.Fatalf("expected 3 sampling buckets (Oct/Nov/Dec), got %d: %v", len(buckets), buckets)
	}
	monthCounts := map[string]int{}
	for _, b := range buckets {
		monthCounts[b.Month] = b.Total
	}
	if monthCounts["2021-10"] != 0 {
		t.Errorf("Oct total = %d, want 0", monthCounts["2021-10"])
	}
	if monthCounts["2021-11"] != 1 {
		t.Errorf("Nov total = %d, want 1", monthCounts["2021-11"])
	}
	if monthCounts["2021-12"] != 0 {
		t.Errorf("Dec total = %d, want 0", monthCounts["2021-12"])
	}
}

// TestSelectPipelines_MultiProjectMerge verifies that calling selectPipelines
// twice with the same prov (two CircleCI project slugs → same repo) merges
// bucket counts rather than producing duplicate month entries.
func TestSelectPipelines_MultiProjectMerge(t *testing.T) {
	inflection := time.Date(2022, 4, 1, 0, 0, 0, 0, time.UTC)
	bw := &config.DurationSpec{Months: 1, Raw: "1m"}
	bracketStart := inflection.AddDate(0, -1, 0) // 2022-03-01
	spec := &config.HistorySampleSpec{Strategy: "monthly", N: 10, Random: false, Raw: "monthly:10"}
	c := &Connector{
		bracketStart: &bracketStart,
		bracketSpec:  bw,
		inflection:   &inflection,
		sampleSpec:   spec,
	}
	window := connector.Window{
		Start: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 4, 30, 0, 0, 0, 0, time.UTC),
	}
	// Project A: 2 Jan pre-bracket pipelines.
	psA := []pipeline{
		{ID: "a1", CreatedAt: time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)},
		{ID: "a2", CreatedAt: time.Date(2022, 1, 20, 0, 0, 0, 0, time.UTC)},
		{ID: "a3", CreatedAt: time.Date(2022, 3, 5, 0, 0, 0, 0, time.UTC)}, // full fidelity
	}
	// Project B: 3 Jan pre-bracket pipelines.
	psB := []pipeline{
		{ID: "b1", CreatedAt: time.Date(2022, 1, 10, 0, 0, 0, 0, time.UTC)},
		{ID: "b2", CreatedAt: time.Date(2022, 1, 15, 0, 0, 0, 0, time.UTC)},
		{ID: "b3", CreatedAt: time.Date(2022, 1, 25, 0, 0, 0, 0, time.UTC)},
	}
	prov := connector.NewProvenance("circleci", "a/b", window)
	prov.Sampling = &connector.SamplingProvenance{}

	c.selectPipelines(psA, "a/b", window, &prov)
	c.selectPipelines(psB, "a/b", window, &prov)

	// Must have exactly one entry per month, not two "2022-01" entries.
	seen := map[string]int{}
	for _, b := range prov.Sampling.Buckets {
		seen[b.Month]++
	}
	for month, count := range seen {
		if count > 1 {
			t.Errorf("month %s appears %d times in Buckets, want 1", month, count)
		}
	}
	// Jan total should be 2+3=5, merged across both projects.
	for _, b := range prov.Sampling.Buckets {
		if b.Month == "2022-01" && b.Total != 5 {
			t.Errorf("2022-01 Total = %d, want 5 (merged across 2 projects)", b.Total)
		}
	}
}
