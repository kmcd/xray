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

// workflow is the slice of CircleCI's workflow payload xray cares about.
type workflow struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	StoppedAt  *time.Time `json:"stopped_at"`
	PipelineID string     `json:"pipeline_id"`
}

type workflowsPage struct {
	Items         []workflow `json:"items"`
	NextPageToken string     `json:"next_page_token"`
}

// listWorkflowsForPipeline walks /pipeline/{id}/workflow pagination.
func (c *Connector) listWorkflowsForPipeline(ctx context.Context, pipelineID string) ([]workflow, bool, error) {
	var out []workflow
	pageToken := ""
	complete := true

	for {
		u, err := url.Parse(c.baseURL + "/pipeline/" + pipelineID + "/workflow")
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
			return out, complete, fmt.Errorf("circleci: list workflows %s: %d", pipelineID, resp.StatusCode)
		}

		var page workflowsPage
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

// buildFromWorkflow maps a workflow + its parent pipeline VCS info to a
// canonical Build row. Pure function; exposed for mapping_test.go.
func buildFromWorkflow(repoSlug string, p pipeline, w workflow) model.Build {
	var startedAt *time.Time
	if !w.CreatedAt.IsZero() {
		t := w.CreatedAt
		startedAt = &t
	}
	completedAt := w.StoppedAt

	var durationSeconds *int
	if startedAt != nil && completedAt != nil {
		d := int(completedAt.Sub(*startedAt).Seconds())
		if d < 0 {
			d = 0
		}
		durationSeconds = &d
	}

	pipelineName := w.Name
	if pipelineName == "" && p.Number > 0 {
		pipelineName = fmt.Sprintf("%d", p.Number)
	}

	return model.Build{
		ID:              w.ID,
		Repo:            repoSlug,
		Source:          "circleci",
		Pipeline:        pipelineName,
		Status:          w.Status,
		Conclusion:      mapWorkflowConclusion(w.Status),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
		DurationSeconds: durationSeconds,
		CommitSHA:       p.VCS.Revision,
		Branch:          p.VCS.Branch,
		Event:           "push",
		Attempt:         1,
		RerunOfID:       "",
		CreatedAt:       w.CreatedAt,
		PRNumber:        nil,
	}
}

// mapWorkflowConclusion folds CircleCI's workflow status enum into the
// canonical conclusion enum (success / failure / cancelled / timed_out /
// skipped / neutral). In-flight states map to "" (no conclusion yet).
func mapWorkflowConclusion(status string) string {
	switch status {
	case "success":
		return "success"
	case "failed", "failing", "error", "unauthorized":
		return "failure"
	case "canceled", "cancelled":
		return "cancelled"
	case "not_run", "skipped":
		return "skipped"
	case "needs_setup":
		return "neutral"
	case "running", "on_hold":
		return ""
	default:
		return ""
	}
}
