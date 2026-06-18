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
// For non-terminal deployments, the associated workflow run conclusion is
// used as a fallback to resolve a terminal status.
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

	// Track per-deployment status-endpoint accessibility across the loop.
	// triedStatuses gates whether we record Endpoints["deploy_statuses"] at
	// all (no deployments in window → no status calls → no entry).
	var triedStatuses, statusesAccessible bool
	statusesAccessible = true
	// triedRunResolve gates whether we record Endpoints["deploy_run_resolve"].
	// Only set when at least one in-flight deployment triggers a workflow-run lookup.
	var triedRunResolve, runResolveAccessible bool
	runResolveAccessible = true

	for {
		if err := ctx.Err(); err != nil {
			prov.Errors["deploys"] = err.Error()
			prov.PaginationComplete = false
			return
		}

		deployments, resp, err := c.client.Repositories.ListDeployments(ctx, owner, name, opts)
		if err != nil {
			prov.Errors["deploys"] = err.Error()
			prov.Endpoints["deployments"] = connector.EndpointStatus{
				Accessible: false,
				Reason:     err.Error(),
			}
			prov.PaginationComplete = false
			return
		}

		for _, d := range deployments {
			if d == nil {
				continue
			}
			createdAt := time.Time{}
			if d.CreatedAt != nil {
				createdAt = d.CreatedAt.UTC()
			}
			if !window.Contains(createdAt) {
				continue
			}

			triedStatuses = true
			state, statusErr := c.latestDeploymentState(ctx, owner, name, int64Of(d.ID))
			if statusErr != nil {
				if prov.Errors["deploy_statuses"] == "" {
					prov.Errors["deploy_statuses"] = statusErr.Error()
				}
				if statusesAccessible {
					statusesAccessible = false
					prov.Endpoints["deploy_statuses"] = connector.EndpointStatus{
						Accessible: false,
						Reason:     statusErr.Error(),
					}
				}
				// Continue with empty status mapped to in_progress.
			}

			// For non-terminal deployments, fall back to the associated
			// workflow run's conclusion to resolve a terminal status.
			// Skip once the endpoint has been marked inaccessible.
			if runResolveAccessible && isNonTerminalState(state) && stringOf(d.SHA) != "" {
				triedRunResolve = true
				if resolved, err := c.resolveStateFromRun(ctx, owner, name, stringOf(d.SHA)); err != nil {
					if runResolveAccessible {
						runResolveAccessible = false
						prov.Endpoints["deploy_run_resolve"] = connector.EndpointStatus{
							Accessible: false,
							Reason:     err.Error(),
						}
					}
				} else if resolved != "" {
					state = resolved
				}
			}

			dep := mapDeploy(d, state, repo.Slug, createdAt)
			if err := sink.InsertDeploy(dep); err != nil {
				prov.Errors["deploys:"+dep.ID] = err.Error()
				continue
			}
			prov.RowsReturned["deploys"]++
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	prov.Endpoints["deployments"] = connector.EndpointStatus{Accessible: true}
	if triedStatuses && statusesAccessible {
		prov.Endpoints["deploy_statuses"] = connector.EndpointStatus{Accessible: true}
	}
	if triedRunResolve && runResolveAccessible {
		prov.Endpoints["deploy_run_resolve"] = connector.EndpointStatus{Accessible: true}
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

// mapDeployStatus maps a GitHub deployment-status state or workflow-run
// conclusion onto the canonical deploys.status set {success, failed, in_progress}.
//
//	success                               -> success
//	failure, error                        -> failed   (deployment status)
//	cancelled, timed_out,                 -> failed   (workflow run conclusion)
//	startup_failure, action_required
//	in_progress, queued, pending, waiting -> in_progress
//	unknown / empty                       -> in_progress
func mapDeployStatus(raw string) string {
	switch raw {
	case "success":
		return "success"
	case "failure", "error", "cancelled", "timed_out", "startup_failure", "action_required":
		return "failed"
	case "in_progress", "queued", "pending", "waiting":
		return "in_progress"
	default:
		return "in_progress"
	}
}

// isNonTerminalState reports whether raw is an in-flight GitHub
// deployment-status state, indicating a terminal status may be available
// from the associated workflow run.
func isNonTerminalState(state string) bool {
	switch state {
	case "in_progress", "queued", "pending", "waiting", "":
		return true
	default:
		return false
	}
}

// resolveStateFromRun returns the GitHub workflow-run conclusion for the
// most recent completed run matching sha (first page, one result). Returns
// ("", nil) when no completed run is found. The caller uses the raw
// conclusion as input to mapDeployStatus.
func (c *Connector) resolveStateFromRun(ctx context.Context, owner, name, sha string) (string, error) {
	runs, _, err := c.client.Actions.ListRepositoryWorkflowRuns(ctx, owner, name,
		&github.ListWorkflowRunsOptions{
			HeadSHA:     sha,
			Status:      "completed",
			ListOptions: github.ListOptions{PerPage: 1},
		},
	)
	if err != nil {
		return "", err
	}
	if runs == nil || len(runs.WorkflowRuns) == 0 || runs.WorkflowRuns[0] == nil {
		return "", nil
	}
	return stringOf(runs.WorkflowRuns[0].Conclusion), nil
}
