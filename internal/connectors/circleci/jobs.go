package circleci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kmcd/xray/internal/model"
)

// job is the slice of CircleCI's workflow-job payload xray cares about.
// CircleCI exposes per-job timestamps inconsistently across endpoints; we
// pick up started_at/stopped_at when present and accept duration may be
// nil otherwise (see doc.go).
type job struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	JobNumber int64      `json:"job_number"`
	StartedAt *time.Time `json:"started_at"`
	StoppedAt *time.Time `json:"stopped_at"`
}

type jobsPage struct {
	Items         []job  `json:"items"`
	NextPageToken string `json:"next_page_token"`
}

// listJobsForWorkflow walks /workflow/{id}/job pagination.
func (c *Connector) listJobsForWorkflow(ctx context.Context, workflowID string) ([]job, bool, error) {
	var out []job
	pageToken := ""
	complete := true

	for {
		u, err := url.Parse(c.baseURL + "/workflow/" + workflowID + "/job")
		if err != nil {
			return out, false, err
		}
		q := u.Query()
		if pageToken != "" {
			q.Set("page-token", pageToken)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return out, false, err
		}
		c.authHeader(req)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			complete = false
			return out, complete, err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			complete = false
			return out, complete, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			complete = false
			return out, complete, fmt.Errorf("circleci: list jobs %s: %d", workflowID, resp.StatusCode)
		}

		var page jobsPage
		if err := json.Unmarshal(body, &page); err != nil {
			complete = false
			return out, complete, err
		}
		out = append(out, page.Items...)

		if page.NextPageToken == "" {
			return out, complete, nil
		}
		pageToken = page.NextPageToken

		if err := ctx.Err(); err != nil {
			return out, false, err
		}
	}
}

// buildJobFromJob maps a CircleCI job onto a canonical BuildJob row. Pure
// function; exposed for mapping_test.go.
func buildJobFromJob(repoSlug, buildID string, j job) model.BuildJob {
	var durationSeconds *int
	if j.StartedAt != nil && j.StoppedAt != nil {
		d := int(j.StoppedAt.Sub(*j.StartedAt).Seconds())
		if d < 0 {
			d = 0
		}
		durationSeconds = &d
	}
	return model.BuildJob{
		BuildID:         buildID,
		Repo:            repoSlug,
		Name:            j.Name,
		Status:          j.Status,
		Conclusion:      mapJobConclusion(j.Status),
		DurationSeconds: durationSeconds,
		Attempt:         1,
	}
}

// mapJobConclusion folds CircleCI's job status enum into the canonical
// conclusion enum. In-flight states map to "" (no conclusion yet).
func mapJobConclusion(status string) string {
	switch status {
	case "success":
		return "success"
	case "failed", "failing", "infrastructure_fail", "terminated-unknown":
		return "failure"
	case "canceled", "cancelled":
		return "cancelled"
	case "not_run", "skipped":
		return "skipped"
	case "timedout":
		return "timed_out"
	case "blocked", "queued", "not_running", "running", "on_hold":
		return ""
	default:
		return ""
	}
}
