package circleci

import (
	"hash/fnv"
	"math/rand"
	"sort"
	"time"

	"github.com/kmcd/xray/internal/connector"
)

// monthBucket holds the UTC date range for one calendar month in the
// pre-bracket sparse slice.
type monthBucket struct {
	Label string    // "YYYY-MM"
	Start time.Time // inclusive (UTC midnight)
	End   time.Time // inclusive (last second of the period)
}

// monthBuckets generates calendar-month buckets covering slice. Bucket
// boundaries are UTC-midnight aligned; the first and last bucket are clipped
// to slice.Start / slice.End respectively.
func monthBuckets(slice connector.Window) []monthBucket {
	var out []monthBucket
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
		out = append(out, monthBucket{
			Label: cur.Format("2006-01"),
			Start: start,
			End:   end,
		})
		cur = next
	}
	return out
}

// bucketSeed returns a deterministic uint64 seed from repo slug + bucket label.
// Each (slug, label) pair gets a distinct seed to avoid correlated bias in
// random mode.
func bucketSeed(slug, label string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(slug))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(label))
	return h.Sum64()
}

// samplePipelines shuffles pipelines with the deterministic seed and returns
// the first n. If len(pipelines) <= n the input is returned unchanged.
func samplePipelines(pipelines []pipeline, n int, seed uint64) []pipeline {
	if len(pipelines) <= n {
		return pipelines
	}
	picked := make([]pipeline, len(pipelines))
	copy(picked, pipelines)
	// #nosec G404 G115 -- deterministic seed; uint64→int64 bit-reinterpretation is intentional.
	rng := rand.New(rand.NewSource(int64(seed)))
	rng.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	return picked[:n]
}

// selectPipelines partitions pipelines into a full-fidelity set (>= bracketStart)
// and a pre-bracket sparse set, samples N per calendar month from the sparse
// set, appends per-bucket SampleBucket records to prov.Sampling, and returns
// the union of full-fidelity + sampled pipelines.
//
// When c.bracketStart is nil (sparse mode disabled) the input slice is returned
// unchanged and prov is not modified.
func (c *Connector) selectPipelines(pipelines []pipeline, repoSlug string, window connector.Window, prov *connector.Provenance) []pipeline {
	if c.bracketStart == nil || c.sampleSpec == nil {
		return pipelines
	}
	bs := *c.bracketStart

	// Partition into full-fidelity (>= bracketStart) and pre-bracket.
	var full, pre []pipeline
	for _, p := range pipelines {
		if !p.CreatedAt.Before(bs) {
			full = append(full, p)
		} else {
			pre = append(pre, p)
		}
	}

	// Group pre-bracket pipelines by month label.
	byMonth := map[string][]pipeline{}
	for _, p := range pre {
		label := p.CreatedAt.UTC().Format("2006-01")
		byMonth[label] = append(byMonth[label], p)
	}

	// Stable sort each month bucket: newest first, ID as tiebreak. This makes
	// both the newest_first slice and the seeded shuffle reproducible
	// independent of API pagination order.
	for label := range byMonth {
		grp := byMonth[label]
		sort.Slice(grp, func(i, j int) bool {
			if !grp[i].CreatedAt.Equal(grp[j].CreatedAt) {
				return grp[i].CreatedAt.After(grp[j].CreatedAt)
			}
			return grp[i].ID < grp[j].ID
		})
		byMonth[label] = grp
	}

	// Walk month buckets in order, sample, record provenance.
	sparseSlice := connector.Window{Start: window.Start, End: bs.Add(-time.Second)}
	selected := full
	for _, b := range monthBuckets(sparseSlice) {
		grp := byMonth[b.Label]
		total := len(grp)
		var picked []pipeline
		switch {
		case c.sampleSpec.Random:
			picked = samplePipelines(grp, c.sampleSpec.N, bucketSeed(repoSlug, b.Label))
		case total > c.sampleSpec.N:
			picked = grp[:c.sampleSpec.N]
		default:
			picked = grp
		}
		selected = append(selected, picked...)
		if prov.Sampling != nil {
			prov.Sampling.Buckets = append(prov.Sampling.Buckets, connector.SampleBucket{
				Month:  b.Label,
				Target: c.sampleSpec.N,
				Actual: len(picked),
				Total:  total,
			})
		}
	}
	return selected
}
