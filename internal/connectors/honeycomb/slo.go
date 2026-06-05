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

// slo is the slice of Honeycomb's SLO payload xray cares about.
type slo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// burnAlert is the slice of Honeycomb's burn-alert payload xray cares about.
//
// `exhaustion_minutes` is the SLO budget exhaustion horizon at the time the
// alert was raised; combined with `created_at` it lets us derive an opened
// timestamp and a coarse severity. We treat short horizons as the most
// urgent.
type burnAlert struct {
	ID                string `json:"id"`
	SLOID             string `json:"slo_id"`
	ExhaustionMinutes int    `json:"exhaustion_minutes"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

// listSLOs lists SLOs on the configured dataset.
func (c *Connector) listSLOs(ctx context.Context) ([]slo, error) {
	u := c.baseURL + "/slos/" + url.PathEscape(c.dataset)
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("honeycomb: list slos %s: %d", c.dataset, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []slo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// listBurnAlerts lists burn alerts on the configured dataset filtered to a
// given SLO id.
func (c *Connector) listBurnAlerts(ctx context.Context, sloID string) ([]burnAlert, error) {
	u, err := url.Parse(c.baseURL + "/burn_alerts/" + url.PathEscape(c.dataset))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("slo_id", sloID)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("honeycomb: list burn_alerts slo=%s: %d", sloID, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []burnAlert
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// burnAlertSeverity classifies a burn alert by exhaustion horizon. Horizons
// shorter than six hours are emitted as "error"; longer or zero/negative
// horizons (already-exhausted budgets fall under the latter shape in some
// Honeycomb configurations) fall back to "warning".
func burnAlertSeverity(exhaustionMinutes int) string {
	if exhaustionMinutes > 0 && exhaustionMinutes < 6*60 {
		return "error"
	}
	return "warning"
}

// burnAlertToIncident maps a Honeycomb burn alert to a canonical
// model.Incident attributed to the supplied repo slug. Returns the zero
// value and false if created_at cannot be parsed (the incident is dropped
// rather than synthesised with a fake timestamp).
func burnAlertToIncident(b burnAlert, repoSlug string) (model.Incident, bool) {
	if b.CreatedAt == "" {
		return model.Incident{}, false
	}
	t, err := time.Parse(time.RFC3339, b.CreatedAt)
	if err != nil {
		return model.Incident{}, false
	}
	return model.Incident{
		ID:       b.ID,
		Repo:     repoSlug,
		Source:   "honeycomb",
		OpenedAt: t.UTC(),
		Severity: burnAlertSeverity(b.ExhaustionMinutes),
	}, true
}

// extractIncidents is best-effort: any error in /slos or /burn_alerts is
// logged at warn level and returns (rows, nil) so the run continues. The
// caller still records partial progress in Provenance.
func (c *Connector) extractIncidents(ctx context.Context, repoSlug string, window connector.Window, sink connector.Sink) int {
	slos, err := c.listSLOs(ctx)
	if err != nil {
		c.log.Warn("honeycomb: skipping SLO burn ingestion",
			"dataset", c.dataset, "err", err.Error())
		return 0
	}

	rows := 0
	for _, s := range slos {
		if err := ctx.Err(); err != nil {
			return rows
		}
		alerts, err := c.listBurnAlerts(ctx, s.ID)
		if err != nil {
			c.log.Warn("honeycomb: skipping burn alerts for slo",
				"slo_id", s.ID, "err", err.Error())
			continue
		}
		for _, a := range alerts {
			inc, ok := burnAlertToIncident(a, repoSlug)
			if !ok {
				continue
			}
			if !window.Contains(inc.OpenedAt) {
				continue
			}
			if err := sink.InsertIncident(inc); err != nil {
				c.log.Warn("honeycomb: insert incident failed",
					"id", inc.ID, "err", err.Error())
				continue
			}
			rows++
		}
	}
	return rows
}
