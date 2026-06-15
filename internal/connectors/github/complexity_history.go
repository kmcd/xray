package github

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sync"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// minPairsPerShard is the minimum number of (sha, path) pairs a shard must
// own before the parallel path is worth the subprocess startup cost. Below
// this threshold the single-shard serial fast-path is used. Exported as a
// var so tests in this package can override it temporarily.
var minPairsPerShard = 64

// extractComplexityHistoryBatch fetches content for all pairs via one or
// more concurrent git cat-file --batch subprocesses and emits
// file_complexity_history rows. When c.extractShards > 1 and the pair
// count is large enough, the pairs are distributed across shards using
// index-stride partitioning (shard s takes pairs[s], pairs[s+S], …).
// Full row coverage is preserved: each pair appears in exactly one shard.
//
// Missing objects are silently skipped. Batch-level errors are recorded in
// prov.Errors but do not abort the connector.
func (c *Connector) extractComplexityHistoryBatch(ctx context.Context, repo connector.Repo, pairs []complexityPair, sink connector.Sink, prov *connector.Provenance) {
	if len(pairs) == 0 || repo.Clone == "" || c.git == nil {
		return
	}

	shards := c.extractShards
	if shards <= 1 || len(pairs) < shards*minPairsPerShard {
		c.catFileBatchOnPairs(ctx, repo, pairs, sink, prov)
		return
	}

	// Parallel path: fan-out across shards with index-stride partition.
	shardProvs := make([]connector.Provenance, shards)
	for s := range shardProvs {
		shardProvs[s] = connector.NewProvenance(c.Name(), repo.Slug, prov.WindowCovered)
	}

	var wg sync.WaitGroup
	wg.Add(shards)
	for s := 0; s < shards; s++ {
		s := s
		sp := &shardProvs[s]
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					if sp.Errors["file_complexity_history"] == "" {
						sp.Errors["file_complexity_history"] = fmt.Sprintf("shard panic: %v", r)
					}
				}
			}()
			// Index-stride: shard s takes pairs[s], pairs[s+shards], …
			slicePairs := make([]complexityPair, 0, (len(pairs)-s+shards-1)/shards)
			for i := s; i < len(pairs); i += shards {
				slicePairs = append(slicePairs, pairs[i])
			}
			c.catFileBatchOnPairs(ctx, repo, slicePairs, sink, sp)
		}()
	}
	wg.Wait()

	for s := range shardProvs {
		prov.Merge(shardProvs[s])
	}
}

// catFileBatchOnPairs is the serial implementation: one git cat-file --batch
// subprocess for the supplied pairs slice. Both the serial fast-path and each
// parallel shard call this.
func (c *Connector) catFileBatchOnPairs(ctx context.Context, repo connector.Repo, pairs []complexityPair, sink connector.Sink, prov *connector.Provenance) {
	if len(pairs) == 0 {
		return
	}
	refs := make([]string, len(pairs))
	for i, p := range pairs {
		refs[i] = p.sha + ":" + p.path
	}

	// file_complexity_history is the largest hot table — batch its writes
	// inside an explicit tx per N rows. Defer Rollback (no-op after a
	// successful Commit). emitDefects / other Cold inserts aren't called
	// from this loop, so no nested-conn deadlock risk.
	fch := openFileComplexityHistoryBatch(sink)
	defer fch.Rollback()

	i := 0
	batchErr := c.git.CatFileBatch(ctx, repo.Clone, refs, func(_ string, content []byte) {
		p := pairs[i]
		i++
		if content == nil {
			return
		}
		stats := scanIndentLevels(content)
		row := model.FileComplexityHistory{
			CommitSHA:   p.sha,
			Repo:        repo.Slug,
			Path:        p.path,
			N:           stats.n,
			IndentTotal: stats.total,
			IndentMean:  stats.mean,
			IndentSD:    stats.sd,
			IndentMax:   stats.maxLevel,
		}
		if err := fch.Add(row); err != nil {
			if prov.Errors["file_complexity_history"] == "" {
				prov.Errors["file_complexity_history"] = err.Error()
			}
			return
		}
	})
	commitBatch(fch, prov, "file_complexity_history")
	if batchErr != nil {
		if prov.Errors["file_complexity_history"] == "" {
			prov.Errors["file_complexity_history"] = batchErr.Error()
		}
		c.log.Warn("file_complexity_history: cat-file batch",
			slog.String("repo", repo.Slug),
			slog.String("error", batchErr.Error()),
		)
	}
}

// complexityPair is a (sha, path) tuple queued for indent-stats computation.
type complexityPair struct {
	sha  string
	path string
}

// indentLevelStats holds the five fields written to file_complexity_history.
// "n" counts only lines with at least one logical indent level — pure
// left-margin lines (level 0) drop out so churn at module scope doesn't
// dilute the mean.
type indentLevelStats struct {
	n        int
	total    int
	mean     float64
	sd       float64
	maxLevel int
}

// scanIndentLevels walks content and aggregates logical-indent statistics
// per the Hindle/Godfrey/Holt 2008 proxy: 4 spaces or 1 tab per level
// (integer division of the raw space-count). indent_sd is the sample
// stddev; emitted as 0.0 when n < 2. Test files, generated files, and
// binaries should be excluded upstream — the scanner runs on whatever it's
// handed.
func scanIndentLevels(content []byte) indentLevelStats {
	var s indentLevelStats
	if len(content) == 0 {
		return s
	}
	var levels []int
	start := 0
	for i := 0; i <= len(content); i++ {
		if i < len(content) && content[i] != '\n' {
			continue
		}
		line := content[start:i]
		start = i + 1
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		if isBlankLine(line) {
			continue
		}
		level := leadingIndent(line) / 4
		if level <= 0 {
			continue
		}
		s.n++
		s.total += level
		if level > s.maxLevel {
			s.maxLevel = level
		}
		levels = append(levels, level)
	}
	if s.n == 0 {
		return s
	}
	s.mean = float64(s.total) / float64(s.n)
	if s.n < 2 {
		return s
	}
	var ss float64
	for _, l := range levels {
		d := float64(l) - s.mean
		ss += d * d
	}
	s.sd = math.Sqrt(ss / float64(s.n-1))
	return s
}

// complexityHistoryExclusionRe mirrors assay's _NONTEST_EXCLUDED_PATH_RE in
// `src/assay_evaluator/stage2/flow.py`. Generated, vendored, dependency-lock,
// binary, and dependency-manifest paths are dropped so the hotspot-trend
// signal isn't drowned by churn we don't care about. Test files are NOT
// excluded — assay computes the test/non-test split downstream.
//
// The regex matches against forward-slash paths exclusively (git paths are
// always slash-separated regardless of OS).
var complexityHistoryExclusionRe = regexp.MustCompile(
	`(?i)` +
		`(^|/)(vendor|node_modules|__pycache__|build|dist|\.venv|venv|target|out|bin)/` +
		`|\.lock$` +
		`|\.generated\.` +
		`|\.pb\.go$` +
		`|_pb2\.py$` +
		`|\.min\.js$` +
		`|\.(png|jpe?g|gif|webp|ico|svg|pdf|zip|tar|gz|tgz|bz2|7z|jar|war|class|so|dll|dylib|exe|bin|wasm|woff2?|ttf|eot|otf|mp4|mov|webm|mp3|wav|ogg)$`,
)

// complexityHistoryExcluded reports whether the path should be skipped from
// the per-revision indent extraction. Exposed for tests.
func complexityHistoryExcluded(path string) bool {
	if path == "" {
		return true
	}
	return complexityHistoryExclusionRe.MatchString(path)
}
