package githubactions

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// deploys pages the Deployments API and emits one model.Deploy per
// deployment. The deploy's status is taken from the latest deployment
// status, mapped onto the canonical {success, failed, in_progress} set.
func (c *Connector) deploys(
	ctx context.Context,
	owner, name string,
	repo connector.Repo,
	window connector.Window,
	sink connector.Sink,
	prov *connector.Provenance,
) {
	opts := &github.DeploymentsListOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		if err := ctx.Err(); err != nil {
			prov.Errors["deploys"] = err.Error()
			prov.PaginationComplete = false
			return
		}

		deployments, resp, err := c.client.Repositories.ListDeployments(ctx, owner, name, opts)
		if err != nil {
			prov.Errors["deploys"] = err.Error()
			prov.PaginationComplete = false
			return
		}

		for _, d := range deployments {
			if d == nil {
				continue
			}
			createdAt := time.Time{}
			if d.CreatedAt != nil {
				createdAt = d.CreatedAt.Time.UTC()
			}
			if !window.Contains(createdAt) {
				continue
			}

			state, statusErr := c.latestDeploymentState(ctx, owner, name, int64Of(d.ID))
			if statusErr != nil {
				prov.Errors["deploy_statuses"] = statusErr.Error()
				// Continue with empty status mapped to in_progress.
			}

			dep := mapDeploy(d, state, repo.Slug, createdAt)
			if err := sink.InsertDeploy(dep); err != nil {
				prov.Errors["deploys"] = err.Error()
				return
			}
			prov.RowsReturned["deploys"]++
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
}

// latestDeploymentState returns the most recent deployment status's state
// string (e.g. "success", "failure", "in_progress", "queued"). Empty when no
// statuses exist.
func (c *Connector) latestDeploymentState(ctx context.Context, owner, name string, deploymentID int64) (string, error) {
	if deploymentID == 0 {
		return "", nil
	}
	// First page sorted by created_at desc is what the GitHub API returns
	// for this endpoint, so the latest is at index 0.
	statuses, _, err := c.client.Repositories.ListDeploymentStatuses(ctx, owner, name, deploymentID, &github.ListOptions{PerPage: 1})
	if err != nil {
		return "", err
	}
	if len(statuses) == 0 || statuses[0] == nil {
		return "", nil
	}
	return stringOf(statuses[0].State), nil
}

// mapDeploy converts a github.Deployment + raw deployment-status state into
// a model.Deploy. Extracted for testability.
func mapDeploy(d *github.Deployment, rawState, repoSlug string, createdAt time.Time) model.Deploy {
	trigger := stringOf(d.Task)
	if trigger == "" {
		trigger = "deploy"
	}
	return model.Deploy{
		ID:          fmt.Sprintf("%d", int64Of(d.ID)),
		Repo:        repoSlug,
		Environment: stringOf(d.Environment),
		DeployedAt:  createdAt,
		CommitSHA:   stringOf(d.SHA),
		Source:      "github_actions",
		Status:      mapDeployStatus(rawState),
		Trigger:     trigger,
		ReleaseTag:  "",
		Version:     stringOf(d.Ref),
	}
}

// mapDeployStatus maps a GitHub deployment-status state onto the canonical
// deploys.status set per the spec.
//
//	success    -> success
//	failure    -> failed
//	error      -> failed   (treated as a failed deploy)
//	in_progress, queued, pending, waiting -> in_progress
//	unknown / empty -> in_progress
func mapDeployStatus(raw string) string {
	switch raw {
	case "success":
		return "success"
	case "failure", "error":
		return "failed"
	case "in_progress", "queued", "pending", "waiting":
		return "in_progress"
	default:
		return "in_progress"
	}
}
