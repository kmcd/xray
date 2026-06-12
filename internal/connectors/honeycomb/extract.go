package honeycomb

import (
	"context"

	"github.com/kmcd/xray/internal/connector"
)

// Extract pulls deploy markers (and best-effort SLO burn alerts) for the
// connector's configured dataset.
//
// Each marker carries a GitHub commit URL; markers are attributed to the repo
// whose slug matches the URL. Markers with no URL or a URL that doesn't match
// the current repo are skipped (logged at debug level). This means every repo
// runs extractDeploys and only receives its own markers.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	deploys, complete, err := c.extractDeploys(ctx, repo.Slug, window, sink, &prov)
	prov.RowsReturned["deploys"] = deploys
	if !complete {
		prov.PaginationComplete = false
	}
	if err != nil {
		prov.Errors["markers"] = err.Error()
		prov.Endpoints["markers"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
	} else {
		prov.Endpoints["markers"] = connector.EndpointStatus{Accessible: true}
	}

	incidents := c.extractIncidents(ctx, repo.Slug, window, sink)
	if incidents > 0 {
		prov.RowsReturned["incidents"] = incidents
	}

	return prov
}
