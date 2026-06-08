package github

import (
	"path/filepath"
	"sort"
	"strings"

	enry "github.com/go-enry/go-enry/v2"
)

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
