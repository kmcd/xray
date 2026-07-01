package circleci

import (
	"math/rand"
	"sort"
	"time"

	"github.com/kmcd/xray/internal/connector"
)

// monthBucket is an alias for the shared connector.MonthBucket.
type monthBucket = connector.MonthBucket

// monthBuckets returns calendar-month buckets covering slice.
// Delegates to connector.MonthBuckets.
func monthBuckets(slice connector.Window) []monthBucket {
	return connector.MonthBuckets(slice)
}

// bucketSeed delegates to connector.BucketSeed.
func bucketSeed(slug, label string) uint64 {
	return connector.BucketSeed(slug, label)
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
			// Merge into an existing bucket for this month if present (two
			// CircleCI project slugs can map to the same repo slug; each call
			// contributes independent pipelines to the same per-repo tally).
			merged := false
			for i := range prov.Sampling.Buckets {
				if prov.Sampling.Buckets[i].Month == b.Label {
					prov.Sampling.Buckets[i].Total += total
					prov.Sampling.Buckets[i].Actual += len(picked)
					merged = true
					break
				}
			}
			if !merged {
				prov.Sampling.Buckets = append(prov.Sampling.Buckets, connector.SampleBucket{
					Month:  b.Label,
					Target: c.sampleSpec.N,
					Actual: len(picked),
					Total:  total,
				})
			}
		}
	}
	return selected
}
