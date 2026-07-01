package githubactions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kmcd/xray/internal/connector"
)

// maxRepoDuration caps the wall-clock time spent extracting a single repo's
// GitHub Actions data. The builds walk is a two-level paginator (workflow runs
// → jobs per run) that, against a degraded API, can enter a legitimate-but-
// unbounded retry loop; this bound limits the blast radius. Provenance records
// the truncation. Mirrors circleci's maxProjectDuration.
const maxRepoDuration = 2 * time.Hour

// Extract pulls workflow runs (builds + build_jobs) and deployments
// (deploys, source=github_actions) for the given repo within window. Errors
// are recorded on the returned provenance rather than panicking; per-table
// row counts are tallied as rows are emitted.
func (c *Connector) Extract(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink) connector.Provenance {
	prov := connector.NewProvenance(c.Name(), repo.Slug, window)

	owner, name, err := splitSlug(repo.Slug)
	if err != nil {
		prov.Errors["repo"] = err.Error()
		return prov
	}

	repoCtx, cancel := context.WithTimeout(ctx, maxRepoDuration)
	defer cancel()

	if err := repoCtx.Err(); err != nil {
		prov.Errors["context"] = err.Error()
		return prov
	}

	c.builds(repoCtx, owner, name, repo, window, sink, &prov)

	// If builds exhausted the per-repo budget (or the parent context was
	// cancelled), skip deploys rather than guaranteeing it fails on its
	// first call and re-records the same truncation.
	if repoCtx.Err() != nil {
		return prov
	}
	c.deploys(repoCtx, owner, name, repo, window, sink, &prov)

	return prov
}

// deadlineHit reports whether ctx hit the per-repo deadline (maxRepoDuration),
// as opposed to a parent cancellation. It gates the timeout WARN only: the
// inaccessible-vs-truncation decision is made on ctx.Err() != nil, so any
// context interruption (deadline or cancel) is treated as truncation and never
// recorded as an inaccessible endpoint (the analyser reads Accessible=false as
// "no signal", which a truncated fetch is not).
func deadlineHit(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// logRepoTimeout emits the operator-facing WARN when the per-repo deadline
// truncates an extraction.
func (c *Connector) logRepoTimeout(repoSlug string) {
	c.log.Warn("github_actions: repo extraction timed out",
		slog.String("repo", repoSlug),
		slog.Duration("timeout", maxRepoDuration),
	)
}

// recordPaginatorError records a failed paginator call on prov. A context
// interruption (the per-repo deadline or a parent cancel) is truncation, not a
// permission denial: it sets Errors+PaginationComplete, logs the timeout WARN
// on a deadline, and leaves the endpoint unmarked — the analyser reads
// Accessible=false as "no signal", which a truncated fetch is not. Any other
// error marks the endpoint inaccessible. Callers return after invoking.
func (c *Connector) recordPaginatorError(ctx context.Context, prov *connector.Provenance, errKey, endpoint, repoSlug string, err error) {
	prov.Errors[errKey] = err.Error()
	prov.PaginationComplete = false
	if ctx.Err() != nil {
		if deadlineHit(ctx) {
			c.logRepoTimeout(repoSlug)
		}
		return
	}
	prov.Endpoints[endpoint] = connector.EndpointStatus{
		Accessible: false,
		Reason:     err.Error(),
	}
}

func splitSlug(slug string) (string, string, error) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo slug %q (want owner/name)", slug)
	}
	return parts[0], parts[1], nil
}
