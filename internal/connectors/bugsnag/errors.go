package bugsnag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// bugsnagError is the wire shape returned by GET /projects/{id}/errors.
// Only fields the connector maps are decoded; the rest is ignored.
type bugsnagError struct {
	ID         string     `json:"id"`
	FirstSeen  time.Time  `json:"first_seen"`
	LastSeen   time.Time  `json:"last_seen"`
	Status     string     `json:"status"`
	Severity   string     `json:"severity"`
	Events     int        `json:"events"`
	ReopenedAt *time.Time `json:"reopened_at"`
	Release    *struct {
		AppVersion string `json:"app_version"`
	} `json:"release"`
}

// listErrors paginates GET /projects/{project_id}/errors over the window and
// emits one Incident per error whose first_seen falls inside the window.
// Returns the number of incidents emitted and whether pagination completed.
// Per-row insert failures are recorded under prov.Errors with a per-incident
// key and do not abort the walk.
func (c *Connector) listErrors(
	ctx context.Context,
	projectID string,
	repoSlug string,
	window connector.Window,
	sink connector.Sink,
	prov *connector.Provenance,
) (rows int, paginationComplete bool, err error) {
	q := url.Values{}
	q.Set("filters[event.since]", window.Start.UTC().Format(time.RFC3339))
	q.Set("filters[event.before]", window.End.UTC().Format(time.RFC3339))
	q.Set("per_page", "100")

	next := fmt.Sprintf("%s/projects/%s/errors?%s",
		c.baseURL, url.PathEscape(projectID), q.Encode())

	for next != "" {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if reqErr != nil {
			return rows, false, reqErr
		}
		c.authHeader(req)

		resp, doErr := c.httpClient.Do(req)
		if doErr != nil {
			return rows, false, doErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return rows, false, fmt.Errorf(
				"bugsnag: list errors project=%s status=%d body=%s",
				projectID, resp.StatusCode, strings.TrimSpace(string(body)),
			)
		}

		body, readErr := io.ReadAll(resp.Body)
		linkHeader := resp.Header.Get("Link")
		_ = resp.Body.Close()
		if readErr != nil {
			return rows, false, fmt.Errorf("bugsnag: read project=%s: %w", projectID, readErr)
		}
		var page []bugsnagError
		if decErr := json.Unmarshal(body, &page); decErr != nil {
			return rows, false, fmt.Errorf("bugsnag: decode project=%s: %w", projectID, decErr)
		}

		for _, e := range page {
			if !window.Contains(e.FirstSeen) {
				continue
			}
			inc := toIncident(e, repoSlug)
			if insErr := sink.InsertIncident(inc); insErr != nil {
				prov.Errors["incidents:"+e.ID] = insErr.Error()
				continue
			}
			rows++
		}

		next = nextLink(linkHeader)
	}

	return rows, true, nil
}

// toIncident maps a Bugsnag error JSON payload to a canonical Incident row.
// Pure function; no I/O. Tested directly in mapping_test.go.
func toIncident(e bugsnagError, repoSlug string) model.Incident {
	inc := model.Incident{
		ID:          e.ID,
		Repo:        repoSlug,
		Source:      "bugsnag",
		OpenedAt:    e.FirstSeen,
		Severity:    e.Severity,
		Occurrences: e.Events,
		// DeployID and CommitSHA wired by M10 from ReleaseRef.
		DeployID:  "",
		CommitSHA: "",
		// AcknowledgedAt has no Bugsnag native equivalent.
		AcknowledgedAt: nil,
		IsRegression:   e.ReopenedAt != nil,
		// CulpritRef intentionally blank per spec — Bugsnag's top stack
		// frame is not an exact equivalent.
		CulpritRef: "",
	}
	if e.Status == "fixed" {
		ls := e.LastSeen
		inc.ResolvedAt = &ls
	}
	if e.Release != nil {
		inc.ReleaseRef = e.Release.AppVersion
	}
	return inc
}

// linkNextRE matches the `<url>; rel="next"` form in a Link header. The
// header may contain other rels (prev, first, last); only next drives
// pagination.
var linkNextRE = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextLink returns the URL of the rel="next" entry in a Link header, or
// the empty string if absent.
func nextLink(header string) string {
	if header == "" {
		return ""
	}
	m := linkNextRE.FindStringSubmatch(header)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
