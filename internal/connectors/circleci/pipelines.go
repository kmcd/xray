package circleci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// pipeline is the slice of CircleCI's pipeline payload xray cares about.
type pipeline struct {
	ID        string    `json:"id"`
	Number    int64     `json:"number"`
	CreatedAt time.Time `json:"created_at"`
	State     string    `json:"state"`
	VCS       struct {
		Revision      string `json:"revision"`
		Branch        string `json:"branch"`
		Tag           string `json:"tag"`
		OriginRepoURL string `json:"origin_repository_url"`
		TargetRepoURL string `json:"target_repository_url"`
	} `json:"vcs"`
}

type pipelinesPage struct {
	Items         []pipeline `json:"items"`
	NextPageToken string     `json:"next_page_token"`
}

// listPipelines walks /project/{slug}/pipeline pagination to completion.
// branch may be empty to fetch all branches. Pipelines older than
// stopBefore (strictly before) short-circuit pagination: CircleCI returns
// pipelines newest-first, so once we observe one older than the window
// start we know all subsequent pages are also out of window.
func (c *Connector) listPipelines(ctx context.Context, projSlug, branch string, stopBefore time.Time) ([]pipeline, bool, error) {
	var out []pipeline
	pageToken := ""
	complete := true

	for {
		u, err := url.Parse(c.baseURL + "/project/" + projSlug + "/pipeline")
		if err != nil {
			return out, false, err
		}
		q := u.Query()
		if branch != "" {
			q.Set("branch", branch)
		}
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
			return out, complete, fmt.Errorf("circleci: list pipelines %s: %d", projSlug, resp.StatusCode)
		}

		var page pipelinesPage
		if err := json.Unmarshal(body, &page); err != nil {
			complete = false
			return out, complete, err
		}

		stop := false
		for _, p := range page.Items {
			if !stopBefore.IsZero() && p.CreatedAt.Before(stopBefore) {
				// Newest-first stream: once an out-of-window pipeline
				// appears, the rest of the stream is older. Stop early.
				stop = true
				continue
			}
			out = append(out, p)
		}

		if stop || page.NextPageToken == "" {
			return out, complete, nil
		}
		pageToken = page.NextPageToken

		if err := ctx.Err(); err != nil {
			return out, false, err
		}
	}
}
