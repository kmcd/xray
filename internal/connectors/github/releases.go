package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractReleases lists releases and emits releases rows plus deploys rows
// (source="github") so the analyser can treat each release as a
// successful deploy when no other deploy source is configured.
//
// Releases whose CreatedAt falls outside the configured window are skipped
// (issue #56). The API does not expose a server-side date filter on this
// endpoint, so we walk pages until we see a release older than the window
// start and stop (releases come back created-at-descending).
func (c *Connector) extractReleases(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return
	}
	opts := &gh.ListOptions{PerPage: 100}
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		rels, resp, err := c.rest.Repositories.ListReleases(ctx, owner, name, opts)
		if err != nil {
			prov.Errors["releases"] = err.Error()
			c.log.Warn("github: list releases",
				slog.String("repo", repo.Slug),
				slog.String("error", err.Error()),
			)
			return
		}
		stopPaging := false
		for _, r := range rels {
			tag := r.GetTagName()
			if tag == "" {
				continue
			}
			createdAt := r.GetCreatedAt().UTC()
			// Releases come back in CreatedAt-desc order. Once we drop
			// before the window start, the remainder of the page (and
			// every subsequent page) is older too.
			if createdAt.Before(window.Start) {
				stopPaging = true
				break
			}
			if createdAt.After(window.End) {
				continue
			}
			sha := resolveReleaseSHA(ctx, c.rest, owner, name, r)

			relRow := model.Release{
				Repo:         repo.Slug,
				Tag:          tag,
				Name:         r.GetName(),
				CreatedAt:    createdAt,
				SHA:          sha,
				IsPrerelease: r.GetPrerelease(),
			}
			if err := sink.InsertRelease(relRow); err != nil {
				if prov.Errors["releases"] == "" {
					prov.Errors["releases"] = err.Error()
				}
			} else {
				prov.RowsReturned["releases"]++
			}

			deployRow := model.Deploy{
				ID:         fmt.Sprintf("release:%s", tag),
				Repo:       repo.Slug,
				DeployedAt: createdAt,
				CommitSHA:  sha,
				Source:     "github",
				Status:     "success",
				ReleaseTag: tag,
				Version:    r.GetName(),
			}
			if err := sink.InsertDeploy(deployRow); err != nil {
				if prov.Errors["deploys"] == "" {
					prov.Errors["deploys"] = err.Error()
				}
			} else {
				prov.RowsReturned["deploys"]++
			}
		}
		if stopPaging || resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
}

// resolveReleaseSHA returns the commit SHA the release's tag points at.
//
// Per issue #57: the previous implementation resolved `r.TargetCommitish`,
// which is typically a branch name like "main" — `GetCommitSHA1` then
// returned the HEAD of that branch, so every release on the same default
// branch stamped the same SHA (whichever HEAD was at extract time).
//
// The correct resolution is the tag itself: GitHub's GetCommitSHA1 accepts
// any ref (branch, tag, or SHA), so we ask it to resolve the tag name. We
// keep TargetCommitish as a fallback for legacy releases whose tag is no
// longer present (rare; usually the tag exists). If TargetCommitish was
// already a full SHA we use it without a round trip.
func resolveReleaseSHA(ctx context.Context, rest *gh.Client, owner, name string, r *gh.RepositoryRelease) string {
	tag := r.GetTagName()
	if tag != "" {
		if sha, _, err := rest.Repositories.GetCommitSHA1(ctx, owner, name, tag, ""); err == nil {
			return strings.ToLower(sha)
		}
	}
	target := r.GetTargetCommitish()
	if isFullSHA(target) {
		return strings.ToLower(target)
	}
	if target == "" {
		return ""
	}
	sha, _, err := rest.Repositories.GetCommitSHA1(ctx, owner, name, target, "")
	if err != nil {
		return ""
	}
	return strings.ToLower(sha)
}

func isFullSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
