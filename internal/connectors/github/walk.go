package github

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	enry "github.com/go-enry/go-enry/v2"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// isBinaryByExt returns true when the file extension is unambiguously binary.
// This allows skipping os.ReadFile without losing row coverage — the
// file_metrics row is still emitted with IsBinary/IsVendored set from the
// pre-classify and LOC fields zeroed.
func isBinaryByExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".svg",
		".pdf",
		".zip", ".tar", ".gz", ".tgz", ".bz2", ".7z",
		".jar", ".war", ".class",
		".so", ".dll", ".dylib", ".exe", ".bin", ".wasm",
		".woff", ".woff2", ".ttf", ".eot", ".otf",
		".mp4", ".mov", ".webm", ".mp3", ".wav", ".ogg":
		return true
	}
	return false
}

// emitRepoLanguages inserts one repo_languages row per language accumulated
// during the walk. Shared by the serial and parallel paths.
func emitRepoLanguages(sink connector.Sink, repo connector.Repo, langTotals map[string]int64, prov *connector.Provenance) {
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

// processWalkFile handles all per-file work: file_metrics, language
// accumulation, and harness detection. It is called from both the serial
// walk callback and the parallel worker goroutines.
func (c *Connector) processWalkFile(
	ctx context.Context,
	repo connector.Repo,
	root string,
	sink connector.Sink,
	prov *connector.Provenance,
	langTotals map[string]int64,
	prog *progress,
	absPath, relPosix string,
	info fs.FileInfo,
) {
	logger := c.log
	if logger == nil {
		logger = slog.Default()
	}

	isVendored := enry.IsVendor(relPosix)
	extBin := isBinaryByExt(relPosix)

	// --- file_metrics ---
	fm := model.FileMetric{
		Repo:        repo.Slug,
		Path:        relPosix,
		SnapshotSHA: repo.HeadSHA,
		SizeBytes:   info.Size(),
	}

	var content []byte

	if isVendored || extBin {
		// Lever 3: skip ReadFile entirely for pre-classified vendor/binary paths.
		// Row is still emitted; LOC fields stay zero.
		fm.IsVendored = isVendored
		fm.IsBinary = extBin
	} else if info.Size() <= maxFileBytes {
		// #nosec G304 -- path is produced by working-tree walk under
		// the per-run clone directory.
		content, _ = os.ReadFile(absPath)
		if content == nil {
			// ReadFile returned nil (should not happen for a regular file, but
			// treat as oversize/binary defensive case).
			fm.IsBinary = true
		} else {
			fm.IsBinary = enry.IsBinary(content)
			fm.IsVendored = isVendored // already computed above; skip re-call
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
	} else {
		// Oversize: emit a minimal row marked binary.
		fm.IsBinary = true
	}

	if err := sink.InsertFileMetric(fm); err != nil {
		if prov.Errors["file_metrics"] == "" {
			prov.Errors["file_metrics"] = err.Error()
		}
	} else {
		prov.RowsReturned["file_metrics"]++
	}
	prog.tick()

	// --- language accumulation ---
	lang := fm.Language
	if lang == "" && content == nil {
		// Oversize file or pre-classified binary/vendor: extension-only fallback.
		lang, _ = enry.GetLanguageByExtension(relPosix)
	}
	if lang != "" {
		langTotals[lang] += info.Size()
	}

	// --- harness ---
	// Skip harness detection for vendored/binary-by-ext paths (content unavailable).
	if isVendored || extBin {
		return
	}
	tool, kind, matched := classifyHarnessPath(relPosix)
	if !matched {
		return
	}
	if isWorkflowPath(relPosix) {
		botTool, ok := detectAIBotInWorkflow(content)
		if !ok {
			return
		}
		tool, kind = botTool, "workflow"
	}
	if c.git == nil {
		return
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
	if err := sink.InsertHarnessArtifact(ha); err != nil {
		if prov.Errors["harness_artifacts"] == "" {
			prov.Errors["harness_artifacts"] = err.Error()
		}
	} else {
		prov.RowsReturned["harness_artifacts"]++
	}
}

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

// walkEntry carries the data a worker goroutine needs to process one file.
type walkEntry struct {
	absPath  string
	relPosix string
	info     fs.FileInfo
}

// walkDecision is the outcome of filterWalkEntry for one Walk callback invocation.
type walkDecision int

const (
	walkProcess walkDecision = iota // regular file — call processWalkFile
	walkSkip                        // soft skip — return nil to Walk
	walkSkipDir                     // prune subtree — return filepath.SkipDir
	walkRootErr                     // fatal root error — return the error to Walk
)

// filterWalkEntry is shared by the serial and parallel Walk callbacks. It
// handles walk error classification, .git/ pruning, and non-regular-file
// filtering. Context cancellation is handled by the callers before this call
// (returning ctx.Err() directly so prov.Errors keys are not set on clean
// shutdown, matching the pre-refactor behaviour).
func filterWalkEntry(root, absPath string, info fs.FileInfo, walkErr error) (relPosix string, dec walkDecision, rootErr error) {
	if walkErr != nil {
		if absPath == root {
			return "", walkRootErr, walkErr
		}
		return "", walkSkip, nil
	}
	rel, relErr := filepath.Rel(root, absPath)
	if relErr != nil {
		return "", walkSkip, nil
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
		if info.IsDir() {
			return "", walkSkipDir, nil
		}
		return "", walkSkip, nil
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return "", walkSkip, nil
	}
	return filepath.ToSlash(rel), walkProcess, nil
}

// extractWorkingTree replaces three separate filepath.Walk passes
// (extractLanguages, fileMetrics, harnessArtifacts) with one. A single walk
// means the kernel page cache is warm for every consumer and per-file syscall
// overhead is paid once. Content is read once per file and shared across all
// three collectors.
//
// When c.extractShards > 1 a producer-consumer pattern parallelises the
// per-file work: the producer goroutine walks the tree and sends entries to a
// bounded channel; c.extractShards worker goroutines drain the channel.
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

	if c.extractShards <= 1 {
		c.extractWorkingTreeSerial(ctx, repo, root, logger, sink, prov)
		return
	}
	c.extractWorkingTreeParallel(ctx, repo, root, logger, sink, prov)
}

// extractWorkingTreeSerial is the single-goroutine fast-path.
func (c *Connector) extractWorkingTreeSerial(ctx context.Context, repo connector.Repo, root string, logger *slog.Logger, sink connector.Sink, prov *connector.Provenance) {
	langTotals := make(map[string]int64)
	prog := newProgress(logger, repo.Slug, "file_metrics")
	defer prog.done()

	_ = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relPosix, dec, rootErr := filterWalkEntry(root, path, info, err)
		switch dec {
		case walkRootErr:
			msg := rootErr.Error()
			prov.Errors["walk"] = msg
			prov.Errors["file_metrics"] = msg
			prov.Errors["harness_artifacts"] = msg
			prov.Errors["repo_languages"] = msg
			return rootErr
		case walkSkipDir:
			return filepath.SkipDir
		case walkSkip:
			if err != nil {
				logger.Debug("walk error", slog.String("path", path), slog.String("err", err.Error()))
			}
			return nil
		}
		c.processWalkFile(ctx, repo, root, sink, prov, langTotals, prog, path, relPosix, info)
		return nil
	})

	emitRepoLanguages(sink, repo, langTotals, prov)
}

// extractWorkingTreeParallel is the producer-consumer parallel path.
func (c *Connector) extractWorkingTreeParallel(ctx context.Context, repo connector.Repo, root string, logger *slog.Logger, sink connector.Sink, prov *connector.Provenance) {
	fileCh := make(chan walkEntry, c.extractShards*4)
	var rootWalkErr error // written by producer, read after both waits

	var producerWg sync.WaitGroup
	producerWg.Add(1)
	go func() {
		defer producerWg.Done()
		defer close(fileCh)
		_ = filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			relPosix, dec, rootErr := filterWalkEntry(root, path, info, err)
			switch dec {
			case walkRootErr:
				rootWalkErr = rootErr
				return rootErr
			case walkSkipDir:
				return filepath.SkipDir
			case walkSkip:
				if err != nil {
					logger.Debug("walk error", slog.String("path", path), slog.String("err", err.Error()))
				}
				return nil
			}
			select {
			case fileCh <- walkEntry{absPath: path, relPosix: relPosix, info: info}:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}()

	workerProvs := make([]connector.Provenance, c.extractShards)
	workerLangs := make([]map[string]int64, c.extractShards)
	for i := range workerProvs {
		workerProvs[i] = connector.NewProvenance(c.Name(), repo.Slug, prov.WindowCovered)
		workerLangs[i] = make(map[string]int64)
	}

	var consumerWg sync.WaitGroup
	for i := 0; i < c.extractShards; i++ {
		consumerWg.Add(1)
		go func(idx int) {
			defer consumerWg.Done()
			wp := &workerProvs[idx]
			wl := workerLangs[idx]
			prog := newProgress(logger, repo.Slug, "file_metrics")
			defer prog.done()
			for e := range fileCh {
				c.processWalkFile(ctx, repo, root, sink, wp, wl, prog, e.absPath, e.relPosix, e.info)
			}
		}(i)
	}

	consumerWg.Wait()
	producerWg.Wait() // producer done after consumers drain; rootWalkErr safe to read now

	if rootWalkErr != nil {
		msg := rootWalkErr.Error()
		prov.Errors["walk"] = msg
		prov.Errors["file_metrics"] = msg
		prov.Errors["harness_artifacts"] = msg
		prov.Errors["repo_languages"] = msg
	}
	for i := range workerProvs {
		prov.Merge(workerProvs[i])
	}
	merged := make(map[string]int64)
	for _, wl := range workerLangs {
		for lang, bytes := range wl {
			merged[lang] += bytes
		}
	}
	emitRepoLanguages(sink, repo, merged, prov)
}
