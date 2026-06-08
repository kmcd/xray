package github

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"regexp"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// emitComplexityHistory writes one file_complexity_history row for a single
// (sha, path) pair using the Hindle/Godfrey/Holt 2008 logical-indent proxy.
// Excluded paths (vendored, generated, dependency manifests, binaries) are
// silently skipped — the row would be noise downstream. A delete entry
// (ChangeType == "D") is skipped because the file doesn't exist at sha;
// the caller filters those before calling.
//
// The signature takes the connector pointer so error / progress logging
// matches the rest of the github extractors. clonePath comes from
// repo.Clone; without it (degraded extraction without a working tree) the
// call returns without an error so commits.go's loop is unaffected.
func emitComplexityHistory(ctx context.Context, c *Connector, repo connector.Repo, sha, path string, sink connector.Sink, prov *connector.Provenance) {
	if repo.Clone == "" || c.git == nil {
		return
	}
	if complexityHistoryExcluded(path) {
		return
	}
	content, err := c.git.ShowFile(ctx, repo.Clone, sha, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Path was deleted at this revision; nothing to count.
			return
		}
		if prov.Errors["file_complexity_history"] == "" {
			prov.Errors["file_complexity_history"] = err.Error()
		}
		c.log.Debug("file_complexity_history: git show",
			slog.String("sha", sha),
			slog.String("path", path),
			slog.String("error", err.Error()),
		)
		return
	}
	stats := scanIndentLevels(content)
	row := model.FileComplexityHistory{
		CommitSHA:   sha,
		Repo:        repo.Slug,
		Path:        path,
		N:           stats.n,
		IndentTotal: stats.total,
		IndentMean:  stats.mean,
		IndentSD:    stats.sd,
		IndentMax:   stats.maxLevel,
	}
	if err := sink.InsertFileComplexityHistory(row); err != nil {
		if prov.Errors["file_complexity_history"] == "" {
			prov.Errors["file_complexity_history"] = err.Error()
		}
		return
	}
	prov.RowsReturned["file_complexity_history"]++
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
