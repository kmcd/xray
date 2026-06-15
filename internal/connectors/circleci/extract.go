package circleci

import (
	"context"
	"log/slog"

	"github.com/kmcd/xray/internal/connector"
)

// Extract pulls workflows + jobs for the configured repo within window and
// emits canonical Build / BuildJob rows via the typed sink. A repo whose
// project_slug is inaccessible to the token is recorded in
// Provenance.Endpoints rather than producing a hard error — per spec,
// inaccessible-vs-absent is a meaningful distinction.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	for ps, rs := range c.projects {
		if rs == repo.Slug {
			c.extractProject(ctx, ps, repo.Slug, window, sink, &prov)
		}
	}

	return prov
}

func (c *Connector) extractProject(ctx context.Context, projSlug, repoSlug string, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	endpointKey := "pipelines:" + projSlug

	// CircleCI returns pipelines newest-first; use window.Start as the
	// short-circuit point so we don't paginate forever into history.
	pipelines, complete, err := c.listPipelines(ctx, projSlug, "", window.Start)
	if err != nil {
		prov.Errors[endpointKey] = err.Error()
		prov.PaginationComplete = false
		// 404 / 401-style failures most often mean the token can't see
		// this project; surface that distinctly in the manifest.
		prov.Endpoints[endpointKey] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
		return
	}
	if !complete {
		prov.PaginationComplete = false
	}
	prov.Endpoints[endpointKey] = connector.EndpointStatus{Accessible: true}

	// Hot-table batches flushed once at the end of this project's walk.
	bB := openBuildsBatch(sink)
	defer bB.Rollback()
	bjB := openBuildJobsBatch(sink)
	defer bjB.Rollback()
	defer func() {
		commitBatch(bB, prov, "builds")
		commitBatch(bjB, prov, "build_jobs")
	}()

	for _, p := range pipelines {
		// Skip pipelines whose created_at falls outside the window. The
		// pipelines call short-circuits on stopBefore; we still need an
		// explicit Contains check to filter future-dated pipelines past
		// window.End.
		if !window.Contains(p.CreatedAt) {
			continue
		}

		workflows, wComplete, err := c.listWorkflowsForPipeline(ctx, p.ID)
		if err != nil {
			prov.Errors["workflows:"+p.ID] = err.Error()
			prov.PaginationComplete = false
			c.log.Warn("circleci: list workflows failed",
				slog.String("pipeline_id", p.ID),
				slog.String("err", err.Error()),
			)
			continue
		}
		if !wComplete {
			prov.PaginationComplete = false
		}

		for _, w := range workflows {
			if !window.Contains(w.CreatedAt) {
				continue
			}

			build := buildFromWorkflow(repoSlug, p, w)
			if err := bB.Add(build); err != nil {
				prov.Errors["build:"+w.ID] = err.Error()
				continue
			}

			jobs, jComplete, err := c.listJobsForWorkflow(ctx, w.ID)
			if err != nil {
				prov.Errors["jobs:"+w.ID] = err.Error()
				prov.PaginationComplete = false
				c.log.Warn("circleci: list jobs failed",
					slog.String("workflow_id", w.ID),
					slog.String("err", err.Error()),
				)
				continue
			}
			if !jComplete {
				prov.PaginationComplete = false
			}

			for _, j := range jobs {
				bj := buildJobFromJob(repoSlug, w.ID, j)
				if err := bjB.Add(bj); err != nil {
					prov.Errors["build_job:"+w.ID+":"+j.Name] = err.Error()
					continue
				}
			}

			if err := ctx.Err(); err != nil {
				prov.PaginationComplete = false
				prov.Errors["ctx"] = err.Error()
				return
			}
		}

		if err := ctx.Err(); err != nil {
			prov.PaginationComplete = false
			prov.Errors["ctx"] = err.Error()
			return
		}
	}
}
