package github

import (
	"context"
	"time"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractReviews emits review rows for a single PR and returns the
// earliest submitted_at across non-PENDING reviews so the caller can
// populate prs.first_review_at.
func (c *Connector) extractReviews(ctx context.Context, repo connector.Repo, number int, sink connector.Sink, prov *connector.Provenance) *time.Time {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return nil
	}
	var first *time.Time
	opts := &gh.ListOptions{PerPage: 100}
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return first
		}
		reviews, resp, err := c.rest.PullRequests.ListReviews(ctx, owner, name, number, opts)
		if err != nil {
			if prov.Errors["reviews"] == "" {
				prov.Errors["reviews"] = err.Error()
			}
			return first
		}
		for _, r := range reviews {
			state := r.GetState()
			if state == "PENDING" {
				continue
			}
			submitted := r.GetSubmittedAt().UTC()
			row := model.Review{
				PRNumber:       number,
				Repo:           repo.Slug,
				ReviewerHandle: r.GetUser().GetLogin(),
				SubmittedAt:    submitted,
				State:          state,
				BodyLength:     len(r.GetBody()),
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
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return first
}
