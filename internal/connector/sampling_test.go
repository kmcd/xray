package connector

import (
	"testing"
	"time"
)

func TestMonthBuckets(t *testing.T) {
	slice := Window{
		Start: time.Date(2022, 1, 15, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 3, 10, 0, 0, 0, 0, time.UTC),
	}
	got := MonthBuckets(slice)
	if len(got) != 3 {
		t.Fatalf("expected 3 buckets, got %d: %+v", len(got), got)
	}
	wantLabels := []string{"2022-01", "2022-02", "2022-03"}
	for i, b := range got {
		if b.Label != wantLabels[i] {
			t.Errorf("bucket[%d].Label = %q, want %q", i, b.Label, wantLabels[i])
		}
	}
	if !got[0].Start.Equal(slice.Start) {
		t.Errorf("bucket[0].Start = %s, want %s", got[0].Start, slice.Start)
	}
	if !got[2].End.Equal(slice.End) {
		t.Errorf("bucket[2].End = %s, want %s", got[2].End, slice.End)
	}
}

func TestMonthBuckets_SingleMonth(t *testing.T) {
	slice := Window{
		Start: time.Date(2022, 6, 5, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2022, 6, 20, 0, 0, 0, 0, time.UTC),
	}
	got := MonthBuckets(slice)
	if len(got) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(got))
	}
	if !got[0].Start.Equal(slice.Start) || !got[0].End.Equal(slice.End) {
		t.Errorf("single-month bucket = {%s..%s}, want {%s..%s}",
			got[0].Start, got[0].End, slice.Start, slice.End)
	}
}

func TestBucketSeed_Deterministic(t *testing.T) {
	s1 := BucketSeed("owner/repo", "2022-01")
	s2 := BucketSeed("owner/repo", "2022-01")
	if s1 != s2 {
		t.Errorf("BucketSeed not deterministic: %d vs %d", s1, s2)
	}
	if BucketSeed("owner/repo", "2022-01") == BucketSeed("owner/repo", "2022-02") {
		t.Error("different labels produced the same seed")
	}
	if BucketSeed("owner/repo", "2022-01") == BucketSeed("owner/other", "2022-01") {
		t.Error("different slugs produced the same seed")
	}
}
