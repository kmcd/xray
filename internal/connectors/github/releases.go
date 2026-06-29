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

// extractReleases is the releases-stage entry point. It prefers a prefetch
// cache hit (populated by prefetchReleases during the run.go clone phase)
// and falls back to a live walk on cache miss or on a cached error. REST
// pagination is opaque, so an errored prefetch does not carry a resume
// cursor — the live fallback restarts from page 1. Releases are emitted
// as both releases and deploys rows (source="github") so the analyser
// can treat each release as a successful deploy when no other deploy
// source is configured.
func (c *Connector) extractReleases(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	nodes, cached, err := c.consumeReleasesPrefetch(ctx, repo.Slug)
	if !cached {
		c.emitReleasesLive(ctx, repo, window, sink, prov)
		return
	}
	if err != nil {
		prov.Errors["releases:prefetch"] = err.Error()
		c.log.Warn("github: releases prefetch errored, falling back to live",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		c.emitReleasesLive(ctx, repo, window, sink, prov)
		return
	}
	c.emitReleases(ctx, repo, window, nodes, sink, prov)
}

// emitReleasesLive walks the releases endpoint live and emits rows in a
// single pass. Used on prefetch miss or when the cached prefetch errored.
// An invalid slug short-circuits to a connector-config endpoint signal
// (Accessible:false, no Errors entry, no PaginationComplete flip) so the
// analyser distinguishes "operator misconfigured this slug" from "endpoint
// reachable but errored mid-walk" — preserving the original extractReleases
// contract.
func (c *Connector) emitReleasesLive(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	if _, _, ok := splitSlug(repo.Slug); !ok {
		prov.Endpoints["releases"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     "invalid slug: " + repo.Slug,
		}
		return
	}
	rels, err := c.fetchAllReleases(ctx, repo.Slug, window)
	if err != nil {
		prov.Errors["releases"] = err.Error()
		prov.PaginationComplete = false
		if ctx.Err() != nil {
			// Context cancelled or deadline exceeded mid-walk: truncation,
			// not a permission denial. Leave the endpoint accessible — the
			// analyser reads Accessible=false as "no signal", which is wrong
			// for an interrupted fetch. Mirrors the circleci cancellation
			// handling (commit 8222a84).
			prov.Endpoints["releases"] = connector.EndpointStatus{Accessible: true}
			return
		}
		prov.Endpoints["releases"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
		c.log.Warn("github: list releases",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		return
	}
	c.emitReleases(ctx, repo, window, rels, sink, prov)
}

// fetchAllReleases walks the releases endpoint and returns in-window
// releases. No row emission and no provenance mutation — both happen in
// emitReleases on the consumer side. Returns (nil, nil) on invalid slug
// to mirror fetchPRs' silent short-circuit; the consumer-side caller
// (emitReleasesLive) records the EndpointStatus separately. Returns
// (collected, ctx.Err()) on cancellation so the caller can treat that
// as a normal error.
//
// Releases whose CreatedAt falls outside the configured window are
// skipped (issue #56). The API does not expose a server-side date filter
// on this endpoint, so we walk pages until we see a release older than
// the window start and stop (releases come back created-at-descending).
func (c *Connector) fetchAllReleases(ctx context.Context, slug string, window connector.Window) ([]*gh.RepositoryRelease, error) {
	owner, name, ok := splitSlug(slug)
	if !ok {
		return nil, nil
	}
	opts := &gh.ListOptions{PerPage: 100}
	var collected []*gh.RepositoryRelease
	for {
		if ctx.Err() != nil {
			return collected, ctx.Err()
		}
		rels, resp, err := c.rest.Repositories.ListReleases(ctx, owner, name, opts)
		if err != nil {
			return collected, err
		}
		stopPaging := false
		for _, r := range rels {
			createdAt := r.GetCreatedAt().UTC()
			// Releases come back in CreatedAt-desc order. Once we drop
			// before the window start, the remainder of the page (and
			// every subsequent page) is older too.
			if createdAt.Before(window.Start) {
				stopPaging = true
				break
			}
			collected = append(collected, r)
		}
		if stopPaging || resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return collected, nil
}

// emitReleases writes releases and deploys rows for the supplied nodes.
// invalid-slug and empty-tag entries are skipped silently; CreatedAt is
// filtered against window.End here (window.Start is handled by
// fetchAllReleases' stop-paging cutoff). On a successful walk
// (rows-or-no-rows) the endpoint is marked Accessible=true so absence
// reads as "endpoint reachable, no in-window releases".
func (c *Connector) emitReleases(ctx context.Context, repo connector.Repo, window connector.Window, rels []*gh.RepositoryRelease, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		prov.Endpoints["releases"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     "invalid slug: " + repo.Slug,
		}
		return
	}
	for _, r := range rels {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		tag := r.GetTagName()
		if tag == "" {
			continue
		}
		createdAt := r.GetCreatedAt().UTC()
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
			ID:           fmt.Sprintf("release:%s", tag),
			Repo:         repo.Slug,
			DeployedAt:   createdAt,
			CommitSHA:    sha,
			Source:       "github",
			Status:       "success",
			ReleaseTag:   tag,
			Version:      r.GetName(),
			IsPrerelease: r.GetPrerelease(),
		}
		if err := sink.InsertDeploy(deployRow); err != nil {
			if prov.Errors["deploys"] == "" {
				prov.Errors["deploys"] = err.Error()
			}
		} else {
			prov.RowsReturned["deploys"]++
		}
	}
	prov.Endpoints["releases"] = connector.EndpointStatus{Accessible: true}
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
	if sha, ok := connector.NormalizeFullSHA(target); ok {
		return sha
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

