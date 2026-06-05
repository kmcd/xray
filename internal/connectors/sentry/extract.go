package sentry

import (
	"context"
	"fmt"

	"github.com/kmcd/xray/internal/connector"
)

// Extract walks every configured (sentry-slug -> repo-slug) entry that
// points at the given repo and emits one `incidents` row per Sentry issue
// whose firstSeen falls inside the window. Repos absent from the mapping
// produce zero rows and a successful (empty) provenance entry.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	matched := false
	for sentrySlug, repoSlug := range c.projects {
		if repoSlug != repo.Slug {
			continue
		}
		matched = true

		issues, complete, err := c.listIssues(ctx, sentrySlug, window)
		if err != nil {
			prov.Errors[sentrySlug] = err.Error()
			prov.PaginationComplete = false
			continue
		}
		if !complete {
			prov.PaginationComplete = false
		}

		for _, iss := range issues {
			inc, ok := mapIssue(iss, repo.Slug, window)
			if !ok {
				continue
			}
			if err := sink.InsertIncident(inc); err != nil {
				prov.Errors[fmt.Sprintf("insert:%s", iss.ID)] = err.Error()
				continue
			}
			prov.RowsReturned["incidents"]++
		}
	}

	if !matched {
		// Repo is not mapped to any Sentry project; nothing to do.
		prov.RowsReturned["incidents"] = 0
	}

	return prov
}
