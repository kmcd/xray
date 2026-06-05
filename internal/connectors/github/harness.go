package github

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// harnessArtifacts inventories AI-tool config files in repo.Clone and
// records their git timeline. Content is captured only when c.capture is
// true (mirrors config.CaptureHarnessContent). See ADR 009 for the
// path -> (tool, kind) mapping.
//
// Assumption about M3's Connector struct: this code reads c.capture, c.git,
// and c.log. If c.git is nil, harnessArtifacts is a no-op.
func harnessArtifacts(ctx context.Context, c *Connector, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	_ = window // adoption timeline is repo-historical, not window-bound
	root := repo.Clone
	if root == "" || c.git == nil {
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
		tool, kind, matched := classifyHarnessPath(relPosix)
		if !matched {
			return nil
		}

		// Workflow files need a content sniff to confirm they invoke a
		// known AI bot; classifyHarnessPath only flags candidacy.
		var content []byte
		if isWorkflowPath(relPosix) {
			content, err = os.ReadFile(path)
			if err != nil {
				return nil
			}
			botTool, ok := detectAIBotInWorkflow(content)
			if !ok {
				return nil
			}
			tool = botTool
			kind = "workflow"
		}

		// Load content if not already loaded and we need it.
		needContent := c.capture
		if needContent && content == nil {
			content, err = os.ReadFile(path)
			if err != nil {
				logger.Debug("harness read error", slog.String("path", relPosix), slog.String("err", err.Error()))
				content = nil
			}
		}

		// LineCount: read content if we haven't.
		if content == nil {
			content, _ = os.ReadFile(path)
		}
		lineCount := countLines(content)

		firstSHA, firstAt, lastAt, gitErr := c.git.LogPath(ctx, root, relPosix)
		if gitErr != nil {
			logger.Debug("harness LogPath error", slog.String("path", relPosix), slog.String("err", gitErr.Error()))
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
}

// classifyHarnessPath maps a working-tree-relative POSIX path to a
// (tool, kind) pair per ADR 009. The third return is false when the path
// is not a harness artifact at all. Workflow files are reported as
// candidates with tool="" — the caller must content-sniff them.
func classifyHarnessPath(p string) (tool, kind string, matched bool) {
	base := filepath.Base(p)

	// claude_code
	if p == "CLAUDE.md" || base == "CLAUDE.md" {
		return "claude_code", "instructions", true
	}
	if p == ".claude" || strings.HasPrefix(p, ".claude/") {
		// Subdirectory determines kind.
		rest := strings.TrimPrefix(p, ".claude/")
		switch {
		case strings.HasPrefix(rest, "rules/"):
			return "claude_code", "rules", true
		case strings.HasPrefix(rest, "skills/"):
			return "claude_code", "skills", true
		case strings.HasPrefix(rest, "agents/"):
			return "claude_code", "agents", true
		case strings.HasPrefix(rest, "commands/"):
			return "claude_code", "commands", true
		default:
			if strings.HasSuffix(strings.ToLower(rest), ".md") {
				return "claude_code", "instructions", true
			}
			return "claude_code", "instructions", true
		}
	}

	// AGENTS.md (unknown tool)
	if p == "AGENTS.md" {
		return "unknown", "instructions", true
	}

	// cursor
	if p == ".cursorrules" {
		return "cursor", "rules", true
	}
	if p == ".cursor/rules" || strings.HasPrefix(p, ".cursor/rules/") {
		return "cursor", "rules", true
	}

	// copilot
	if p == ".github/copilot-instructions.md" {
		return "copilot", "instructions", true
	}

	// aider
	if p == "aider.conf.yml" || strings.HasPrefix(p, ".aider") {
		return "aider", "rules", true
	}

	// windsurf
	if p == ".windsurfrules" {
		return "windsurf", "rules", true
	}

	// continue
	if p == ".continue" || strings.HasPrefix(p, ".continue/") {
		return "continue", "rules", true
	}

	// generic MCP
	if p == ".mcp.json" || base == "mcp.json" {
		return "generic_mcp", "mcp", true
	}

	// workflow candidate (caller content-sniffs)
	if isWorkflowPath(p) {
		return "", "workflow", true
	}

	return "", "", false
}

// isWorkflowPath returns true for files under .github/workflows with a yml
// or yaml extension.
func isWorkflowPath(p string) bool {
	if !strings.HasPrefix(p, ".github/workflows/") {
		return false
	}
	lower := strings.ToLower(p)
	return strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")
}

// aiBots is the set of bot identifiers that, when present in workflow
// content, mark the workflow as a harness artifact. Returned tool is the
// matched identifier.
var aiBots = []struct {
	needle string
	tool   string
}{
	{"coderabbitai", "coderabbit"},
	{"claude", "claude_code"},
	{"copilot", "copilot"},
	{"cursor-agent", "cursor"},
}

// detectAIBotInWorkflow searches workflow content for an AI bot reference.
// First match wins, in the order defined by aiBots.
func detectAIBotInWorkflow(content []byte) (string, bool) {
	lower := bytes.ToLower(content)
	for _, b := range aiBots {
		if bytes.Contains(lower, []byte(b.needle)) {
			return b.tool, true
		}
	}
	return "", false
}

// countLines returns the number of newline-delimited lines in b. A file
// without a trailing newline still counts its final partial line.
func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}
