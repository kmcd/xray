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

// extractRepoFiles inserts one repo_file row per file tracked at HEAD via
// git ls-files --cached. .gitignore is honoured by git's index; .git/ is
// never listed. Symlinks are recorded as regular entries; their targets are
// not followed. Provenance increments repo_file once per inserted row.
func (c *Connector) extractRepoFiles(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
	if repo.Clone == "" || c.git == nil {
		return
	}
	paths, err := c.git.LsFiles(ctx, repo.Clone)
	if err != nil {
		prov.Errors["repo_file"] = err.Error()
		return
	}
	for _, p := range paths {
		if err := sink.InsertRepoFile(model.RepoFile{Repo: repo.Slug, Path: p}); err != nil {
			if prov.Errors["repo_file"] == "" {
				prov.Errors["repo_file"] = err.Error()
			}
		} else {
			prov.RowsReturned["repo_file"]++
		}
	}
}

// extractWorkingTree replaces three separate filepath.Walk passes
// (extractLanguages, fileMetrics, harnessArtifacts) with one. A single walk
// means the kernel page cache is warm for every consumer and per-file syscall
// overhead is paid once. Content is read once per file and shared across all
// three collectors.
func (c *Connector) extractWorkingTree(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	_ = window // adopted here; harness timeline is repo-historical, not window-bound
	root := repo.Clone
	if root == "" {
		return
	}
	logger := c.log
	if logger == nil {
		logger = slog.Default()
	}

	langTotals := make(map[string]int64)
	prog := newProgress(logger, repo.Slug, "file_metrics")
	defer prog.done()

	_ = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if path == root {
				prov.Errors["walk"] = err.Error()
				return err
			}
			logger.Debug("walk error", slog.String("path", path), slog.String("err", err.Error()))
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
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		relPosix := filepath.ToSlash(rel)

		// Read content once for all consumers. Oversize files get a nil
		// slice; each consumer falls back to metadata-only paths.
		var content []byte
		if info.Size() <= maxFileBytes {
			// #nosec G304 -- path is produced by working-tree walk under
			// the per-run clone directory.
			content, _ = os.ReadFile(path)
		}

		// --- file_metrics ---
		fm := model.FileMetric{
			Repo:        repo.Slug,
			Path:        relPosix,
			SnapshotSHA: repo.HeadSHA,
			SizeBytes:   info.Size(),
		}
		if content == nil {
			// Oversize: emit a minimal row marked binary.
			fm.IsBinary = true
		} else {
			fm.IsBinary = enry.IsBinary(content)
			fm.IsVendored = enry.IsVendor(relPosix)
			fm.IsGenerated = enry.IsGenerated(relPosix, content)
			fm.IsTest = isTestPath(relPosix)
			fm.IsDependencyManifest = isDependencyManifest(relPosix)
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
		}
		if err := sink.InsertFileMetric(fm); err == nil {
			prov.RowsReturned["file_metrics"]++
		}
		prog.tick()

		// --- language accumulation (reuse language already computed above) ---
		lang := fm.Language
		if lang == "" && content == nil {
			// Oversize file: extension-only fallback.
			lang, _ = enry.GetLanguageByExtension(relPosix)
		}
		if lang != "" {
			langTotals[lang] += info.Size()
		}

		// --- harness ---
		tool, kind, matched := classifyHarnessPath(relPosix)
		if !matched {
			return nil
		}
		if isWorkflowPath(relPosix) {
			botTool, ok := detectAIBotInWorkflow(content)
			if !ok {
				return nil
			}
			tool, kind = botTool, "workflow"
		}
		if c.git == nil {
			return nil
		}
		lineCount := countLines(content)
		firstSHA, firstAt, lastAt, gitErr := c.git.LogPath(ctx, root, relPosix)
		if gitErr != nil {
			logger.Debug("harness LogPath error",
				slog.String("path", relPosix),
				slog.String("err", gitErr.Error()),
			)
		}
		ha := model.HarnessArtifact{
			Repo:            repo.Slug,
			Path:            relPosix,
			Tool:            tool,
			Kind:            kind,
			LineCount:       lineCount,
			FirstSeenCommit: firstSHA,
			FirstSeenAt:     firstAt,
			LastModifiedAt:  lastAt,
		}
		if c.capture {
			ha.Content = string(content)
		}
		if err := sink.InsertHarnessArtifact(ha); err == nil {
			prov.RowsReturned["harness_artifacts"]++
		}
		return nil
	})

	// Emit accumulated language rows after the walk completes.
	for lang, bytes := range langTotals {
		row := model.RepoLanguage{Repo: repo.Slug, Language: lang, Bytes: bytes}
		if err := sink.InsertRepoLanguage(row); err != nil {
			if prov.Errors["repo_languages"] == "" {
				prov.Errors["repo_languages"] = err.Error()
			}
			continue
		}
		prov.RowsReturned["repo_languages"]++
	}
}
