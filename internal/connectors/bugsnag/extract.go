package bugsnag

import (
	"context"
	"time"

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
	ew := c.cappedWindow(window)
	prov := connector.NewProvenance(c.Name(), repo.Slug, ew)
	prov.RowsReturned["incidents"] = 0
	// Record the effective incident window in ConfigDepth when the cap actually
	// narrowed the global window. The analyser reads the date range to interpret
	// reduced incident row counts as "out of scope" rather than "no signal."
	// Mirrors the pr_window pattern in the GitHub connector.
	if ew.Start.After(window.Start) {
		if prov.ConfigDepth == nil {
			prov.ConfigDepth = make(map[string]string)
		}
		prov.ConfigDepth["max_window_days"] = ew.Start.Format("2006-01-02") + ".." + ew.End.Format("2006-01-02")
	}

	for projectID, mappedSlug := range c.projects {
		if mappedSlug != repo.Slug {
			continue
		}
		rows, complete, err := c.listErrors(ctx, projectID, repo.Slug, ew, sink, &prov)
		prov.RowsReturned["incidents"] += rows
		if !complete {
			prov.PaginationComplete = false
		}
		if err != nil {
			prov.Errors["project:"+projectID] = err.Error()
			prov.Endpoints["project:"+projectID] = connector.EndpointStatus{
				Accessible: false,
				Reason:     err.Error(),
			}
		} else {
			prov.Endpoints["project:"+projectID] = connector.EndpointStatus{Accessible: true}
		}
	}

	return prov
}

// cappedWindow returns a connector.Window whose Start is no earlier than
// maxWindowDays before w.End, capping the query at the plan's retention
// horizon. If the configured global window is already shorter than the cap,
// the original window is returned unchanged.
func (c *Connector) cappedWindow(w connector.Window) connector.Window {
	if c.maxWindowDays <= 0 {
		return w
	}
	cutoff := w.End.Add(-time.Duration(c.maxWindowDays) * 24 * time.Hour)
	if cutoff.After(w.Start) {
		return connector.Window{Start: cutoff, End: w.End}
	}
	return w
}
