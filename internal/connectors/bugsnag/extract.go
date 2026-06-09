package bugsnag

import (
	"context"

	"github.com/kmcd/xray/internal/connector"
)

// Extract pulls incidents for `repo` from every Bugsnag project mapped to
// this repo's slug in the connector's project map. Per-project errors are
// recorded but do not abort the overall extraction; provenance carries the
// per-project failure message.
func (c *Connector) Extract(
	ctx context.Context,
	repo connector.Repo,
	window connector.Window,
	sink connector.Sink,
) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)
	prov.RowsReturned["incidents"] = 0

	for projectID, mappedSlug := range c.projects {
		if mappedSlug != repo.Slug {
			continue
		}
		rows, complete, err := c.listErrors(ctx, projectID, repo.Slug, window, sink, &prov)
		prov.RowsReturned["incidents"] += rows
		if !complete {
			prov.PaginationComplete = false
		}
		if err != nil {
			prov.Errors["project:"+projectID] = err.Error()
		}
	}

	return prov
}
