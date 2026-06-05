package githubactions

import (
	"context"
	"fmt"
	"strings"

	"github.com/kmcd/xray/internal/connector"
)

// Extract pulls workflow runs (builds + build_jobs) and deployments
// (deploys, source=github_actions) for the given repo within window. Errors
// are recorded on the returned provenance rather than panicking; per-table
// row counts are tallied as rows are emitted.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	owner, name, err := splitSlug(repo.Slug)
	if err != nil {
		prov.Errors["repo"] = err.Error()
		return prov
	}

	if err := ctx.Err(); err != nil {
		prov.Errors["context"] = err.Error()
		return prov
	}

	c.builds(ctx, owner, name, repo, window, sink, &prov)
	c.deploys(ctx, owner, name, repo, window, sink, &prov)

	return prov
}

func splitSlug(slug string) (string, string, error) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo slug %q (want owner/name)", slug)
	}
	return parts[0], parts[1], nil
}
