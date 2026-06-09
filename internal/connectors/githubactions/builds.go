package githubactions

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// builds pages workflow runs in window, emits one model.Build per run, and
// fans out to ListWorkflowJobs for the per-run build_jobs rows.
func (c *Connector) builds(
	ctx context.Context,
	owner, name string,
	repo connector.Repo,
	window connector.Window,
	sink connector.Sink,
	prov *connector.Provenance,
) {
	// GitHub's Created filter accepts a date range: YYYY-MM-DD..YYYY-MM-DD.
	created := fmt.Sprintf("%s..%s",
		window.Start.UTC().Format("2006-01-02"),
		window.End.UTC().Format("2006-01-02"),
	)

	opts := &github.ListWorkflowRunsOptions{
		Created:     created,
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		if err := ctx.Err(); err != nil {
			prov.Errors["builds"] = err.Error()
			prov.PaginationComplete = false
			return
		}

		runs, resp, err := c.client.Actions.ListRepositoryWorkflowRuns(ctx, owner, name, opts)
		if err != nil {
			prov.Errors["builds"] = err.Error()
			prov.PaginationComplete = false
			return
		}
		if runs == nil {
			break
		}

		for _, run := range runs.WorkflowRuns {
			if run == nil {
				continue
			}
			b := mapBuild(run, repo.Slug)
			// Filter by window using CreatedAt; the Created filter is a
			// coarse pre-filter and runs may land at the boundary.
			if !window.Contains(b.CreatedAt) {
				continue
			}
			if err := sink.InsertBuild(b); err != nil {
				prov.Errors["builds:"+b.ID] = err.Error()
				continue
			}
			prov.RowsReturned["builds"]++

			c.jobsForRun(ctx, owner, name, repo.Slug, run, sink, prov)
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
}

// jobsForRun pages the jobs for a single workflow run and emits build_jobs.
func (c *Connector) jobsForRun(
	ctx context.Context,
	owner, name, repoSlug string,
	run *github.WorkflowRun,
	sink connector.Sink,
	prov *connector.Provenance,
) {
	runID := int64Of(run.ID)
	if runID == 0 {
		return
	}

	jOpts := &github.ListWorkflowJobsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		if err := ctx.Err(); err != nil {
			prov.Errors["build_jobs"] = err.Error()
			prov.PaginationComplete = false
			return
		}
		jobs, resp, err := c.client.Actions.ListWorkflowJobs(ctx, owner, name, runID, jOpts)
		if err != nil {
			prov.Errors["build_jobs"] = err.Error()
			prov.PaginationComplete = false
			return
		}
		if jobs == nil {
			break
		}
		for _, j := range jobs.Jobs {
			if j == nil {
				continue
			}
			bj := mapBuildJob(j, runID, repoSlug, runAttempt(run))
			if err := sink.InsertBuildJob(bj); err != nil {
				prov.Errors[fmt.Sprintf("build_jobs:%d:%s", runID, bj.Name)] = err.Error()
				continue
			}
			prov.RowsReturned["build_jobs"]++
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		jOpts.Page = resp.NextPage
	}
}

// mapBuild converts a github.WorkflowRun to a model.Build. Extracted as a
// pure function for testability.
//
// rerun_of_id heuristic: when run.RunAttempt > 1 the same run ID identifies
// the originating run (every attempt of a workflow run shares the same ID;
// the API exposes attempt-specific data via GetWorkflowRunAttempt). We
// record the run ID itself as RerunOfID for Attempt > 1, which lets a
// downstream consumer group same-SHA reruns without an extra API call.
func mapBuild(run *github.WorkflowRun, repoSlug string) model.Build {
	id := fmt.Sprintf("%d", int64Of(run.ID))
	attempt := runAttempt(run)

	var startedAt, completedAt *time.Time
	if run.RunStartedAt != nil {
		t := run.RunStartedAt.UTC()
		startedAt = &t
	}
	if run.UpdatedAt != nil {
		t := run.UpdatedAt.UTC()
		completedAt = &t
	}
	var durationSeconds *int
	if startedAt != nil && completedAt != nil {
		d := int(completedAt.Sub(*startedAt).Seconds())
		if d < 0 {
			d = 0
		}
		durationSeconds = &d
	}

	createdAt := time.Time{}
	if run.CreatedAt != nil {
		createdAt = run.CreatedAt.UTC()
	}

	var prNumber *int
	if len(run.PullRequests) > 0 && run.PullRequests[0] != nil && run.PullRequests[0].Number != nil {
		n := *run.PullRequests[0].Number
		prNumber = &n
	}

	rerunOf := ""
	if attempt > 1 {
		rerunOf = id
	}

	return model.Build{
		ID:              id,
		Repo:            repoSlug,
		Source:          "github_actions",
		Pipeline:        stringOf(run.Name),
		Status:          stringOf(run.Status),
		Conclusion:      stringOf(run.Conclusion),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
		DurationSeconds: durationSeconds,
		CommitSHA:       stringOf(run.HeadSHA),
		Branch:          stringOf(run.HeadBranch),
		Event:           stringOf(run.Event),
		Attempt:         attempt,
		RerunOfID:       rerunOf,
		CreatedAt:       createdAt,
		PRNumber:        prNumber,
	}
}

// mapBuildJob converts a github.WorkflowJob to a model.BuildJob. Extracted
// for testability.
func mapBuildJob(j *github.WorkflowJob, runID int64, repoSlug string, attempt int) model.BuildJob {
	var duration *int
	if j.StartedAt != nil && j.CompletedAt != nil {
		d := int(j.CompletedAt.Time.Sub(j.StartedAt.Time).Seconds())
		if d < 0 {
			d = 0
		}
		duration = &d
	}
	return model.BuildJob{
		BuildID:         fmt.Sprintf("%d", runID),
		Repo:            repoSlug,
		Name:            stringOf(j.Name),
		Status:          stringOf(j.Status),
		Conclusion:      stringOf(j.Conclusion),
		DurationSeconds: duration,
		Attempt:         attempt,
	}
}

func runAttempt(run *github.WorkflowRun) int {
	if run == nil || run.RunAttempt == nil {
		return 1
	}
	if *run.RunAttempt < 1 {
		return 1
	}
	return *run.RunAttempt
}

func stringOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func int64Of(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
