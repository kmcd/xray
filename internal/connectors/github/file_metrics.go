package github

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	enry "github.com/go-enry/go-enry/v2"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// fileMetrics walks the working tree at repo.Clone (snapshot = repo.HeadSHA)
// and emits a model.FileMetric per file. Skips the .git directory; follows
// symlinks via filepath.Walk's default behaviour (treats them as their
// stat()'d target). Files larger than maxFileBytes are emitted with only
// path and size populated and IsBinary=true to avoid loading huge blobs.
//
// Assumption about M3's Connector struct: this code only reads c.log. If
// c.log is nil it falls back to a discard logger to avoid a nil deref.
func fileMetrics(ctx context.Context, c *Connector, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
	root := repo.Clone
	if root == "" {
		return
	}
	logger := c.log
	if logger == nil {
		logger = slog.Default()
	}

	_ = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			logger.Debug("file_metrics walk error", slog.String("path", path), slog.String("err", err.Error()))
			return nil
		}
		// Compute path relative to clone root.
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		// Skip .git directory entirely.
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			// skip sockets, fifos, devices; symlinks are followed by Walk
			// when they target regular files (Walk uses Lstat — but our
			// resolution above checks the target via Stat). Practically:
			// non-regular entries are skipped here.
			return nil
		}

		relPosix := filepath.ToSlash(rel)
		fm := model.FileMetric{
			Repo:        repo.Slug,
			Path:        relPosix,
			SnapshotSHA: repo.HeadSHA,
			SizeBytes:   info.Size(),
		}

		// Oversize: emit a minimal row marked binary; don't read content.
		if info.Size() > maxFileBytes {
			fm.IsBinary = true
			if err := sink.InsertFileMetric(fm); err == nil {
				prov.RowsReturned["file_metrics"]++
			}
			return nil
		}

		// #nosec G304 -- path is produced by the working-tree walk under the
		// per-run clone directory.
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			logger.Debug("file_metrics read error", slog.String("path", relPosix), slog.String("err", readErr.Error()))
			// Emit metadata-only row.
			if err := sink.InsertFileMetric(fm); err == nil {
				prov.RowsReturned["file_metrics"]++
			}
			return nil
		}

		fm.IsBinary = enry.IsBinary(content)
		fm.IsVendored = enry.IsVendor(relPosix)
		fm.IsGenerated = enry.IsGenerated(relPosix, content)
		fm.IsTest = isTestPath(relPosix)
		fm.IsDependencyManifest = isDependencyManifest(relPosix)

		// Language: extension first, then content classifier.
		fm.Language = languageFor(relPosix, content, fm.IsBinary)

		if !fm.IsBinary {
			stats := scanLines(content)
			fm.LOCTotal = stats.total
			fm.LOCCode = stats.code
			fm.LOCBlank = stats.blank
			fm.MaxIndent = stats.maxIndent
			fm.MeanIndent = stats.meanIndent
			fm.MaxLineLength = stats.maxLineLen
			fm.P95LineLength = stats.p95LineLen
		}

		if err := sink.InsertFileMetric(fm); err == nil {
			prov.RowsReturned["file_metrics"]++
		}
		return nil
	})
}

// maxFileBytes is the per-file size ceiling for full inspection. Files
// above this are recorded as binary with size/path only.
const maxFileBytes int64 = 5 * 1024 * 1024

// languageFor resolves a file's language using go-enry. Tries extension
// first; falls back to content for non-binary files <=1MB.
func languageFor(path string, content []byte, isBinary bool) string {
	if lang, _ := enry.GetLanguageByExtension(path); lang != "" {
		return lang
	}
	if isBinary {
		return ""
	}
	if int64(len(content)) > 1024*1024 {
		return ""
	}
	lang, _ := enry.GetLanguageByContent(path, content)
	return lang
}

// isTestPath returns true when the path matches conventional test markers:
// *_test.*, *.test.*, *.spec.*, contains /spec/, contains /__tests__/.
func isTestPath(p string) bool {
	lower := strings.ToLower(p)
	if strings.Contains(lower, "/__tests__/") || strings.HasPrefix(lower, "__tests__/") {
		return true
	}
	if strings.Contains(lower, "/spec/") || strings.HasPrefix(lower, "spec/") {
		return true
	}
	base := filepath.Base(lower)
	// strip extension(s) to look at stem
	// Handle the common single-extension cases: foo_test.go, foo.test.ts, foo.spec.rb.
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, "_test") {
		return true
	}
	if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return true
	}
	return false
}

// dependencyManifestNames is the static set from ADR 008. Match is
// case-sensitive on basename.
var dependencyManifestNames = map[string]struct{}{
	"Gemfile":            {},
	"Gemfile.lock":       {},
	"package.json":       {},
	"package-lock.json":  {},
	"yarn.lock":          {},
	"pnpm-lock.yaml":     {},
	"go.mod":             {},
	"go.sum":             {},
	"Cargo.toml":         {},
	"Cargo.lock":         {},
	"requirements.txt":   {},
	"Pipfile":            {},
	"Pipfile.lock":       {},
	"poetry.lock":        {},
	"pyproject.toml":     {},
	"composer.json":      {},
	"composer.lock":      {},
	"pom.xml":            {},
	"build.gradle":       {},
	"build.gradle.kts":   {},
	"Podfile":            {},
	"Podfile.lock":       {},
	"mix.exs":            {},
	"mix.lock":           {},
}

func isDependencyManifest(p string) bool {
	_, ok := dependencyManifestNames[filepath.Base(p)]
	return ok
}

type lineStats struct {
	total      int
	code       int
	blank      int
	maxIndent  int
	meanIndent float64
	maxLineLen int
	p95LineLen int
}

// scanLines walks the file content one line at a time and aggregates LOC,
// indentation and line-length statistics. Tabs count as 4 spaces for
// indentation. Line length is measured in bytes.
func scanLines(content []byte) lineStats {
	var (
		s            lineStats
		indentSum    int
		indentLines  int
	)
	lineLens := make([]int, 0, 256)

	// Split by '\n'. A trailing newline produces an empty final element
	// which we don't count as an extra line. If the file is empty, zero
	// lines.
	if len(content) == 0 {
		return s
	}

	start := 0
	for i := 0; i <= len(content); i++ {
		if i == len(content) || content[i] == '\n' {
			lineBytes := content[start:i]
			// Strip a trailing CR for CRLF files.
			if n := len(lineBytes); n > 0 && lineBytes[n-1] == '\r' {
				lineBytes = lineBytes[:n-1]
			}
			// If we're at EOF and the last byte was a newline, the
			// "line" after that newline is a synthetic empty; skip it.
			if i == len(content) && i > 0 && content[i-1] == '\n' {
				start = i + 1
				continue
			}

			s.total++
			lineLens = append(lineLens, len(lineBytes))
			if len(lineBytes) > s.maxLineLen {
				s.maxLineLen = len(lineBytes)
			}

			if isBlankLine(lineBytes) {
				s.blank++
			} else {
				s.code++
				ind := leadingIndent(lineBytes)
				if ind > s.maxIndent {
					s.maxIndent = ind
				}
				indentSum += ind
				indentLines++
			}
			start = i + 1
		}
	}

	if indentLines > 0 {
		s.meanIndent = float64(indentSum) / float64(indentLines)
	}
	s.p95LineLen = percentile(lineLens, 95)
	return s
}

func isBlankLine(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

// leadingIndent counts leading spaces, with tabs counted as 4 spaces.
func leadingIndent(b []byte) int {
	n := 0
	for _, c := range b {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// percentile returns the p-th percentile (0-100) of vals using nearest-rank.
func percentile(vals []int, p int) int {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int, len(vals))
	copy(sorted, vals)
	sort.Ints(sorted)
	// nearest-rank: ceil(p/100 * N), 1-indexed
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
