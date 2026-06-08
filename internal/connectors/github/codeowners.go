package github

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// codeownersPaths lists, in priority order, the locations GitHub recognises
// for a CODEOWNERS file. We probe each via the working-tree clone until one
// reads successfully; absence at every path is treated as "no codeowners"
// with the endpoint marked accessible (the filesystem is always reachable
// once the clone exists).
var codeownersPaths = []string{
	".github/CODEOWNERS",
	"CODEOWNERS",
	"docs/CODEOWNERS",
}

func (c *Connector) extractCodeowners(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
	_ = ctx
	if repo.Clone == "" {
		return
	}
	prov.Endpoints["codeowners"] = connector.EndpointStatus{Accessible: true}

	var content string
	for _, p := range codeownersPaths {
		// #nosec G304 -- path is the per-run clone directory joined with a
		// hardcoded candidate from codeownersPaths.
		b, err := os.ReadFile(filepath.Join(repo.Clone, p))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			c.log.Warn("github: read codeowners",
				slog.String("repo", repo.Slug),
				slog.String("path", p),
				slog.String("error", err.Error()),
			)
			continue
		}
		content = string(b)
		break
	}
	if content == "" {
		return
	}

	for _, row := range parseCodeowners(repo.Slug, content) {
		if err := sink.InsertCodeowner(row); err != nil {
			if prov.Errors["codeowners"] == "" {
				prov.Errors["codeowners"] = err.Error()
			}
			continue
		}
		prov.RowsReturned["codeowners"]++
	}
}

// parseCodeowners parses CODEOWNERS file content into Codeowner rows. The
// format is one rule per line: "<pattern> @user @org/team ...". Lines
// starting with "#" and blank lines are skipped. Owner strings are
// classified as "team" when they contain a slash, otherwise "user". The
// leading "@" is stripped from the stored handle.
func parseCodeowners(repo, content string) []model.Codeowner {
	var rows []model.Codeowner
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip inline comments (everything after an unescaped #).
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pattern := fields[0]
		for _, owner := range fields[1:] {
			handle := strings.TrimPrefix(owner, "@")
			if handle == "" {
				continue
			}
			ownerType := "user"
			if strings.Contains(handle, "/") {
				ownerType = "team"
			}
			rows = append(rows, model.Codeowner{
				Repo:        repo,
				Pattern:     pattern,
				OwnerHandle: handle,
				OwnerType:   ownerType,
			})
		}
	}
	return rows
}
