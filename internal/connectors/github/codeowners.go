package github

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// codeownersPaths lists, in priority order, the locations GitHub recognises
// for a CODEOWNERS file. We probe each via GetContents until one returns
// 200; absence at every path is treated as "no codeowners" with the
// endpoint marked accessible (we could read, there was just nothing there).
var codeownersPaths = []string{
	".github/CODEOWNERS",
	"CODEOWNERS",
	"docs/CODEOWNERS",
}

func (c *Connector) extractCodeowners(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return
	}
	prov.Endpoints["codeowners"] = connector.EndpointStatus{Accessible: true}

	var content string
	for _, p := range codeownersPaths {
		file, _, resp, err := c.rest.Repositories.GetContents(ctx, owner, name, p, nil)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				continue
			}
			c.log.Warn("github: get codeowners",
				slog.String("repo", repo.Slug),
				slog.String("path", p),
				slog.String("error", err.Error()),
			)
			continue
		}
		if file == nil {
			continue
		}
		s, err := file.GetContent()
		if err != nil {
			continue
		}
		content = s
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
