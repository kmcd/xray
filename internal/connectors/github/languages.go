package github

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	enry "github.com/go-enry/go-enry/v2"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractLanguages walks the working tree at repo.Clone and emits a
// repo_languages row per detected language with the on-disk byte total.
// Replaces the prior REST ListLanguages call so we don't depend on an
// API endpoint for a stat we can compute locally from the clone.
func (c *Connector) extractLanguages(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) error {
	root := repo.Clone
	if root == "" {
		return nil
	}
	logger := c.log
	if logger == nil {
		logger = slog.Default()
	}

	totals := make(map[string]int64)

	_ = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			logger.Debug("languages walk error", slog.String("path", path), slog.String("err", err.Error()))
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
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
			return nil
		}

		relPosix := filepath.ToSlash(rel)

		// Try extension first without reading the file.
		lang := languageFor(relPosix, nil, false)
		if lang == "" && info.Size() <= 1024*1024 {
			// Fall back to content classifier for small files only.
			// #nosec G304 -- path is produced by the working-tree walk under
			// the per-run clone directory.
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				logger.Debug("languages read error", slog.String("path", relPosix), slog.String("err", readErr.Error()))
				return nil
			}
			lang = languageFor(relPosix, content, enry.IsBinary(content))
			// content drops out of scope here — no source bytes persist.
		}
		if lang == "" {
			return nil
		}
		totals[lang] += info.Size()
		return nil
	})

	for lang, bytes := range totals {
		row := model.RepoLanguage{Repo: repo.Slug, Language: lang, Bytes: bytes}
		if err := sink.InsertRepoLanguage(row); err != nil {
			if prov.Errors["repo_languages"] == "" {
				prov.Errors["repo_languages"] = err.Error()
			}
			continue
		}
		prov.RowsReturned["repo_languages"]++
	}
	return nil
}
