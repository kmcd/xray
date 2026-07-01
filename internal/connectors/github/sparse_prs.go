package github

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
)

// prSearchQuery pages the GraphQL search() connection for PRs in a date range.
// GitHub search returns results in relevance order by default.
type prSearchQuery struct {
	Search struct {
		IssueCount githubv4.Int
		PageInfo   struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []struct {
			PullRequest prGraph `graphql:"... on PullRequest"`
		}
	} `graphql:"search(query: $query, type: ISSUE, first: $first, after: $after)"`
}

// monthBucket is an alias for the shared connector.MonthBucket, used locally
// within sparse_prs to avoid verbose package prefixes throughout this file.
type monthBucket = connector.MonthBucket

// bucketResult is the output of one bucket fetch goroutine.
type bucketResult struct {
	meta  connector.SampleBucket
	nodes []prGraph
	err   error
}

// monthBuckets returns calendar-month buckets covering slice.
// Delegates to connector.MonthBuckets; defined as a local alias so callers
// within this file don't need the package prefix.
func monthBuckets(slice connector.Window) []monthBucket {
	return connector.MonthBuckets(slice)
}

// weeklyBucketsFor splits one month bucket into 7-day sub-buckets. Used when
// totalCount > searchTruncationCap. No further recursion beyond weekly.
func weeklyBucketsFor(m monthBucket) []monthBucket {
	var out []monthBucket
	cur := m.Start
	wk := 1
	for !cur.After(m.End) {
		next := cur.AddDate(0, 0, 7)
		end := next.Add(-time.Second)
		if end.After(m.End) {
			end = m.End
		}
		out = append(out, monthBucket{
			Label: fmt.Sprintf("%s-W%d", m.Label, wk),
			Start: cur,
			End:   end,
		})
		cur = next
		wk++
	}
	return out
}

// bucketSeed delegates to connector.BucketSeed.
func bucketSeed(slug, label string) uint64 {
	return connector.BucketSeed(slug, label)
}

// randomPickN shuffles nodes with the deterministic seed and returns the
// first n. Returns the original slice when len(nodes) <= n.
func randomPickN(nodes []prGraph, n int, seed uint64) []prGraph {
	if len(nodes) <= n {
		return nodes
	}
	picked := make([]prGraph, len(nodes))
	copy(picked, nodes)
	// #nosec G404 G115 -- deterministic seed; uint64→int64 bit-reinterpretation is intentional.
	rng := rand.New(rand.NewSource(int64(seed)))
	rng.Shuffle(len(picked), func(i, j int) { picked[i], picked[j] = picked[j], picked[i] })
	return picked[:n]
}

// searchTruncationCap is the GitHub search() result cap. Buckets exceeding
// this are split to weekly sub-buckets and marked truncated in provenance.
const searchTruncationCap = 1000

// searchPRsInRange issues GraphQL search() calls for PRs created within
// [start, end] on the repo. Paginates until limit nodes are collected or
// there are no more results. Returns (nodes, totalCount, err); totalCount is
// the IssueCount reported by GitHub on the first page (the full population
// size for the query, not just the returned slice).
func (c *Connector) searchPRsInRange(ctx context.Context, slug string, start, end time.Time, limit int) ([]prGraph, int, error) {
	owner, name, ok := splitSlug(slug)
	if !ok {
		return nil, 0, nil
	}
	q := fmt.Sprintf("is:pr repo:%s/%s created:%s..%s",
		owner, name,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
	)

	perPage := limit
	if perPage > 100 {
		perPage = 100
	}

	vars := map[string]any{
		"query": githubv4.String(q),
		"first": githubv4.Int(perPage),
		"after": (*githubv4.String)(nil),
	}

	var all []prGraph
	var totalCount int
	firstPage := true

	for {
		if ctx.Err() != nil {
			return all, totalCount, ctx.Err()
		}
		var sq prSearchQuery
		if err := c.queryWithEOFRetry(ctx, &sq, vars); err != nil {
			return all, totalCount, err
		}
		if firstPage {
			totalCount = int(sq.Search.IssueCount)
			firstPage = false
		}
		for _, n := range sq.Search.Nodes {
			all = append(all, n.PullRequest)
			if len(all) >= limit {
				return all, totalCount, nil
			}
		}
		if !bool(sq.Search.PageInfo.HasNextPage) {
			break
		}
		vars["after"] = githubv4.NewString(sq.Search.PageInfo.EndCursor)
	}
	return all, totalCount, nil
}

// fetchBucket fetches PRs for one month (or week) bucket and returns one or
// more bucketResults. When totalCount > searchTruncationCap the bucket is
// split into weekly sub-buckets; the parent result is returned with
// Truncated=true and no nodes, followed by the weekly sub-results.
func (c *Connector) fetchBucket(ctx context.Context, slug string, b monthBucket, spec *config.HistorySampleSpec) []bucketResult {
	nodes, total, err := c.searchPRsInRange(ctx, slug, b.Start, b.End, spec.N)

	meta := connector.SampleBucket{
		Month:  b.Label,
		Target: spec.N,
		Total:  total,
	}

	if err != nil {
		meta.Actual = len(nodes)
		return []bucketResult{{meta: meta, nodes: nodes, err: err}}
	}

	if total > searchTruncationCap {
		meta.Truncated = true
		c.log.Warn("github: sparse: search bucket exceeds 1000-result cap; splitting to weekly",
			slog.String("repo", slug),
			slog.String("bucket", b.Label),
			slog.Int("total_count", total),
		)
		results := []bucketResult{{meta: meta}}
		weeks := weeklyBucketsFor(b)
		weekTarget := (spec.N + len(weeks) - 1) / len(weeks)
		weekSpec := *spec
		weekSpec.N = weekTarget
		for _, w := range weeks {
			results = append(results, c.fetchBucketLeaf(ctx, slug, w, &weekSpec))
		}
		return results
	}

	if spec.Random {
		nodes = randomPickN(nodes, spec.N, bucketSeed(slug, b.Label))
	}
	meta.Actual = len(nodes)
	return []bucketResult{{meta: meta, nodes: nodes}}
}

// fetchBucketLeaf fetches one sub-bucket without further splitting. Used for
// weekly sub-buckets produced by the truncation path; weekly → daily recursion
// is not supported. When a weekly bucket also exceeds the cap, it is marked
// truncated and the capped results are kept.
func (c *Connector) fetchBucketLeaf(ctx context.Context, slug string, b monthBucket, spec *config.HistorySampleSpec) bucketResult {
	nodes, total, err := c.searchPRsInRange(ctx, slug, b.Start, b.End, spec.N)
	meta := connector.SampleBucket{
		Month:  b.Label,
		Target: spec.N,
		Total:  total,
	}
	if err != nil {
		meta.Actual = len(nodes)
		return bucketResult{meta: meta, nodes: nodes, err: err}
	}
	// Truncation check comes after the error check: a network failure can
	// return a partial totalCount that exceeds the cap without meaning the
	// search cap was actually hit. Only flag Truncated on a clean response.
	if total > searchTruncationCap {
		meta.Truncated = true
		c.log.Warn("github: sparse: weekly bucket also exceeds 1000-result cap; capping at search limit",
			slog.String("repo", slug),
			slog.String("bucket", b.Label),
			slog.Int("total_count", total),
		)
	}
	if spec.Random {
		nodes = randomPickN(nodes, spec.N, bucketSeed(slug, b.Label))
	}
	meta.Actual = len(nodes)
	return bucketResult{meta: meta, nodes: nodes}
}

const sparseBucketConcurrency = 4

// extractSparsePRs fetches and emits PRs in the pre-bracket sparse slice.
// Month buckets are fetched up to sparseBucketConcurrency concurrently; a
// serial aggregator collects all results (sorted by month label for stable
// provenance) and calls emitPR, so batch handles are never written
// concurrently. prov.Sampling must be non-nil when called.
func (c *Connector) extractSparsePRs(ctx context.Context, repo connector.Repo, slice connector.Window, spec *config.HistorySampleSpec, sink connector.Sink, prov *connector.Provenance) {
	buckets := monthBuckets(slice)
	if len(buckets) == 0 {
		return
	}

	// Buffer large enough for all results; producers never block even if
	// the consumer is slow. len(buckets)*6 covers the worst case where
	// every month splits into 5 weekly sub-buckets plus a parent record.
	resultCh := make(chan []bucketResult, len(buckets)*6)
	sem := make(chan struct{}, sparseBucketConcurrency)

	var wg sync.WaitGroup
	for _, b := range buckets {
		wg.Add(1)
		go func(b monthBucket) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					// Send nil so the consumer is not blocked waiting for this
					// bucket. Appending a nil slice is a no-op. The bucket
					// omission is visible in the final row counts.
					resultCh <- nil
				}
			}()
			resultCh <- c.fetchBucket(ctx, repo.Slug, b, spec)
		}(b)
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Drain the channel and sort by month label for stable provenance output.
	var all []bucketResult
	for results := range resultCh {
		all = append(all, results...)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].meta.Month < all[j].meta.Month
	})

	tpl, err := c.fetchTemplate(ctx, repo.Slug, prov)
	if err != nil {
		c.log.Warn("github: sparse: fetch PR template",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
	}

	prsB := openPRsBatch(sink)
	defer prsB.Rollback()
	prcB := openPRCommitsBatch(sink)
	defer prcB.Rollback()
	prlB := openPRLabelsBatch(sink)
	defer prlB.Rollback()
	revB := openReviewsBatch(sink)
	defer revB.Rollback()
	cmtB := openPRCommentsBatch(sink)
	defer cmtB.Rollback()

	prog := newProgress(c.log, repo.Slug, "prs_sample")
	defer prog.done()

outer:
	for _, r := range all {
		if r.err != nil {
			key := fmt.Sprintf("prs_sample:%s", r.meta.Month)
			if prov.Errors[key] == "" {
				prov.Errors[key] = r.err.Error()
			}
			prov.PaginationComplete = false
		}
		for _, p := range r.nodes {
			if ctx.Err() != nil {
				prov.PaginationComplete = false
				break outer
			}
			c.emitPR(ctx, repo, p, tpl, sink, prsB, prcB, prlB, revB, cmtB, prov)
			prog.tick()
		}
		if prov.Sampling != nil {
			prov.Sampling.Buckets = append(prov.Sampling.Buckets, r.meta)
		}
	}

	commitBatch(prsB, prov, "prs")
	commitBatch(prcB, prov, "pr_commits")
	commitBatch(prlB, prov, "pr_labels")
	commitBatch(revB, prov, "reviews")
	commitBatch(cmtB, prov, "pr_comments")
}
