package github

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// emitReviewsInline emits review rows from the inline GraphQL connection on
// a PR and returns the earliest submitted_at across non-PENDING reviews.
// Body strings are length-measured and discarded — never persisted.
func emitReviewsInline(nodes []reviewGraph, prNum int, slug string, sink connector.Sink, prov *connector.Provenance) *time.Time {
	var first *time.Time
	for _, r := range nodes {
		if strings.EqualFold(string(r.State), "PENDING") {
			continue
		}
		if r.SubmittedAt == nil {
			continue
		}
		submitted := r.SubmittedAt.UTC()
		row := model.Review{
			PRNumber:       prNum,
			Repo:           slug,
			ReviewerHandle: hashHandle(canonicalLogin(string(r.Author.Login))),
			SubmittedAt:    submitted,
			State:          string(r.State),
			BodyLength:     len(string(r.Body)),
		}
		if err := sink.InsertReview(row); err != nil {
			if prov.Errors["reviews"] == "" {
				prov.Errors["reviews"] = err.Error()
			}
		} else {
			prov.RowsReturned["reviews"]++
		}
		if first == nil || submitted.Before(*first) {
			t := submitted
			first = &t
		}
	}
	return first
}

// paginatePRReviewsOverflow drains additional review pages for a PR whose
// inline Reviews.PageInfo.HasNextPage was true. Best-effort: an error
// during pagination leaves the PR's review_count unchanged on the row
// (the PR row records the GraphQL TotalCount, not the emitted count).
func (c *Connector) paginatePRReviewsOverflow(ctx context.Context, owner, name string, number int, slug, cursor string, sink connector.Sink, prov *connector.Provenance) *time.Time {
	var first *time.Time
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return first
		}
		var q struct {
			Repository struct {
				PullRequest struct {
					Reviews struct {
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage githubv4.Boolean
						}
						Nodes []reviewGraph
					} `graphql:"reviews(first: 100, after: $after)"`
				} `graphql:"pullRequest(number: $number)"`
			} `graphql:"repository(owner: $owner, name: $name)"`
		}
		vars := map[string]any{
			"owner": githubv4.String(owner),
			"name":  githubv4.String(name),
			// #nosec G115 -- PR numbers fit comfortably in int32.
			"number": githubv4.Int(int32(number)),
			"after":  githubv4.String(cursor),
		}
		if err := c.gql.Query(ctx, &q, vars); err != nil {
			if prov.Errors["reviews"] == "" {
				prov.Errors["reviews"] = err.Error()
			}
			c.log.Warn("github: graphql reviews overflow",
				slog.String("repo", slug),
				slog.Int("pr", number),
				slog.String("error", err.Error()),
			)
			return first
		}
		if t := emitReviewsInline(q.Repository.PullRequest.Reviews.Nodes, number, slug, sink, prov); t != nil {
			if first == nil || t.Before(*first) {
				first = t
			}
		}
		if !bool(q.Repository.PullRequest.Reviews.PageInfo.HasNextPage) {
			return first
		}
		cursor = string(q.Repository.PullRequest.Reviews.PageInfo.EndCursor)
	}
}
