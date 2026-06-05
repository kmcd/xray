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
func (c *Connector) extractReleases(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
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
		for _, r := range rels {
			tag := r.GetTagName()
			if tag == "" {
				continue
			}
			createdAt := r.GetCreatedAt().Time.UTC()
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
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
}

// resolveReleaseSHA tries to return a full commit SHA for a release. If the
// release's TargetCommitish is already a full SHA we use it directly;
// otherwise we ask the API to resolve it (TargetCommitish may be a branch
// name like "main"). Falls back to empty string on any failure.
func resolveReleaseSHA(ctx context.Context, rest *gh.Client, owner, name string, r *gh.RepositoryRelease) string {
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
