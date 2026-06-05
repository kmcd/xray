package honeycomb

import (
	"context"

	"github.com/kmcd/xray/internal/connector"
)

// Extract pulls deploy markers (and best-effort SLO burn alerts) for the
// connector's configured dataset.
//
// Honeycomb has no per-repo concept: the first Extract call wins and owns
// all emitted rows under its repo slug. Subsequent Extract calls return
// an empty Provenance with the skip recorded under endpoints["markers"]
// so the manifest reflects the reason no data was returned for that repo.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	if !c.chooseRepo(repo.Slug) {
		prov.Endpoints["markers"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     "honeycomb has no per-repo concept; emitted under " + c.chosenRepo(),
		}
		return prov
	}

	deploys, complete, err := c.extractDeploys(ctx, repo.Slug, window, sink)
	prov.RowsReturned["deploys"] = deploys
	if !complete {
		prov.PaginationComplete = false
	}
	if err != nil {
		prov.Errors["markers"] = err.Error()
	}

	incidents := c.extractIncidents(ctx, repo.Slug, window, sink)
	if incidents > 0 {
		prov.RowsReturned["incidents"] = incidents
	}

	return prov
}
