package honeycomb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// marker is the slice of Honeycomb's marker payload xray cares about.
//
// See https://docs.honeycomb.io/api/tag/Markers — start_time/end_time are
// epoch seconds in the wire format, decoded as int64 and converted to UTC
// time.Time on read.
type marker struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	StartTime  int64  `json:"start_time"`
	EndTime    int64  `json:"end_time"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	Color      string `json:"color"`
	DatasetTag string `json:"dataset"`
}

// listMarkers fetches all markers for the configured dataset. The endpoint
// returns a flat array with no server-side date filter, so all markers are
// fetched and filtered client-side in extractDeploys. Volume grows with
// deployment frequency; no pagination to manage.
func (c *Connector) listMarkers(ctx context.Context) ([]marker, error) {
	u := c.baseURL + "/markers/" + url.PathEscape(c.dataset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("honeycomb: 401 listing markers")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("honeycomb: list markers %s: %d", c.dataset, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []marker
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// markerToDeploy maps a Honeycomb marker to a canonical model.Deploy
// attributed to the supplied repo slug. Pure function: kept separate from
// HTTP plumbing so the mapping can be unit-tested without a fake server.
func markerToDeploy(m marker, repoSlug string) model.Deploy {
	return model.Deploy{
		ID:                 m.ID,
		Repo:               repoSlug,
		Environment:        m.Type,
		DeployedAt:         time.Unix(m.StartTime, 0).UTC(),
		CommitSHA:          "",
		Source:             "honeycomb",
		Status:             "success",
		SupersedesDeployID: "",
		RolledBack:         false,
		Trigger:            "",
		ReleaseTag:         "",
		Version:            m.Message,
	}
}

// extractDeploys lists markers, filters to the window, and emits one Deploy
// per in-window marker. Returns (rowsEmitted, paginationComplete, error) where
// error is non-nil only for the list-call / context failures. Per-row insert
// failures are recorded into prov.Errors with per-id keys and do not abort
// the walk.
func (c *Connector) extractDeploys(ctx context.Context, repoSlug string, window connector.Window, sink connector.Sink, prov *connector.Provenance) (int, bool, error) {
	markers, err := c.listMarkers(ctx)
	if err != nil {
		return 0, false, err
	}

	rows := 0
	for _, m := range markers {
		if err := ctx.Err(); err != nil {
			return rows, false, err
		}
		if m.StartTime == 0 {
			continue
		}
		t := time.Unix(m.StartTime, 0).UTC()
		if !window.Contains(t) {
			continue
		}
		d := markerToDeploy(m, repoSlug)
		if err := sink.InsertDeploy(d); err != nil {
			prov.Errors["deploys:"+m.ID] = err.Error()
			continue
		}
		rows++
	}
	return rows, true, nil
}
