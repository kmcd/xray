package circleci

import (
	"context"
	"errors"
	"log/slog"
	"time"

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

// maxProjectDuration caps the wall-clock time spent extracting a single
// CircleCI project. Multi-level pagination (pipelines → workflows → jobs) on a
// large project with a degraded API can enter a legitimate-but-unbounded retry
// loop; this bound limits the blast radius. Provenance records the truncation.
const maxProjectDuration = 30 * time.Minute

func (c *Connector) extractProject(ctx context.Context, projSlug, repoSlug string, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	projCtx, cancel := context.WithTimeout(ctx, maxProjectDuration)
	defer cancel()

	endpointKey := "pipelines:" + projSlug

	// CircleCI returns pipelines newest-first; use window.Start as the
	// short-circuit point so we don't paginate forever into history.
	pipelines, complete, err := c.listPipelines(projCtx, projSlug, "", window.Start)
	if err != nil {
		prov.Errors[endpointKey] = err.Error()
		prov.PaginationComplete = false
		if errors.Is(projCtx.Err(), context.DeadlineExceeded) {
			// Timeout during initial fetch: truncation, not a permission
			// denial. Do not mark the endpoint inaccessible — the analyser
			// treats Accessible=false as "no signal", which would be wrong.
			c.log.Warn("circleci: project extraction timed out",
				slog.String("project", projSlug),
				slog.Duration("timeout", maxProjectDuration),
			)
		} else {
			// 404 / 401-style failures most often mean the token can't see
			// this project; surface that distinctly in the manifest.
			prov.Endpoints[endpointKey] = connector.EndpointStatus{
				Accessible: false,
				Reason:     err.Error(),
			}
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
		if err := projCtx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				c.log.Warn("circleci: project extraction timed out",
					slog.String("project", projSlug),
					slog.Duration("timeout", maxProjectDuration),
				)
			}
			prov.PaginationComplete = false
			prov.Errors["ctx:"+projSlug] = err.Error()
			return
		}

		// Skip pipelines whose created_at falls outside the window. The
		// pipelines call short-circuits on stopBefore; we still need an
		// explicit Contains check to filter future-dated pipelines past
		// window.End.
		if !window.Contains(p.CreatedAt) {
			continue
		}

		workflows, wComplete, err := c.listWorkflowsForPipeline(projCtx, p.ID)
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

			jobs, jComplete, err := c.listJobsForWorkflow(projCtx, w.ID)
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

			if err := projCtx.Err(); err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					c.log.Warn("circleci: project extraction timed out",
						slog.String("project", projSlug),
						slog.Duration("timeout", maxProjectDuration),
					)
				}
				prov.PaginationComplete = false
				prov.Errors["ctx:"+projSlug] = err.Error()
				return
			}
		}

		if err := projCtx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				c.log.Warn("circleci: project extraction timed out",
					slog.String("project", projSlug),
					slog.Duration("timeout", maxProjectDuration),
				)
			}
			prov.PaginationComplete = false
			prov.Errors["ctx:"+projSlug] = err.Error()
			return
		}
	}
}
