package connector

import (
	"hash/fnv"
	"time"
)

// MonthBucket holds the UTC date range for one calendar month (or a weekly
// sub-bucket) in a sparse-historical sampling slice.
type MonthBucket struct {
	Label string    // "YYYY-MM" or "YYYY-MM-W1" for weekly sub-buckets
	Start time.Time // inclusive (UTC midnight)
	End   time.Time // inclusive (last second of the period)
}

// MonthBuckets generates calendar-month buckets covering slice. Bucket
// boundaries are UTC-midnight aligned; the first and last bucket are clipped
// to slice.Start / slice.End respectively.
func MonthBuckets(slice Window) []MonthBucket {
	var out []MonthBucket
	cur := time.Date(slice.Start.Year(), slice.Start.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !cur.After(slice.End) {
		next := cur.AddDate(0, 1, 0)
		end := next.Add(-time.Second)
		if end.After(slice.End) {
			end = slice.End
		}
		start := cur
		if start.Before(slice.Start) {
			start = slice.Start
		}
		out = append(out, MonthBucket{
			Label: cur.Format("2006-01"),
			Start: start,
			End:   end,
		})
		cur = next
	}
	return out
}

// BucketSeed returns a deterministic uint64 seed from repo slug + bucket label.
// Each (slug, label) pair gets a distinct seed to avoid correlated bias in
// random mode.
func BucketSeed(slug, label string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(slug))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(label))
	return h.Sum64()
}
