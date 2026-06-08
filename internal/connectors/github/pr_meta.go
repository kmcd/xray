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

// reviewRequestedEvent matches the GraphQL shape we expect on a
// ReviewRequestedEvent node. Promoted to a named type so both the inline
// timelineNode in prs.go and the overflow paginator below can reference it.
type reviewRequestedEvent struct {
	CreatedAt         githubv4.DateTime
	RequestedReviewer struct {
		User struct {
			Login githubv4.String
		} `graphql:"... on User"`
		Team struct {
			CombinedSlug githubv4.String
		} `graphql:"... on Team"`
	}
}

type reviewRequestNode struct {
	Typename             githubv4.String      `graphql:"__typename"`
	ReviewRequestedEvent reviewRequestedEvent `graphql:"... on ReviewRequestedEvent"`
}

// emitTimelineDerived walks the PR's inline timelineItems, returning the
// PR-row derived fields (readyForReviewAt, firstReviewAtTL,
// forcePushedAfterReview) AND emitting pr_review_requests rows from any
// REVIEW_REQUESTED_EVENT entries it encounters.
//
// Timeline nodes are returned in chronological order, so the inline
// first-100 reliably captures the earliest of each event type on all but
// the longest-tail PRs. Overflow is handled by paginatePRReviewRequestsOverflow
// (only for additional REVIEW_REQUESTED entries; derived fields use the
// inline minimum).
func emitTimelineDerived(nodes []timelineNode, prNum int, slug string, sink connector.Sink, prov *connector.Provenance) (readyForReviewAt, firstReviewAtTL *time.Time, forcePushedAfterReview bool) {
	var firstForcePush *time.Time
	for _, t := range nodes {
		switch t.Typename {
		case "ReadyForReviewEvent":
			tt := t.ReadyForReviewEvent.CreatedAt.UTC()
			if readyForReviewAt == nil || tt.Before(*readyForReviewAt) {
				readyForReviewAt = &tt
			}
		case "PullRequestReview":
			if strings.EqualFold(string(t.PullRequestReview.State), "PENDING") {
				continue
			}
			tt := t.PullRequestReview.CreatedAt.UTC()
			if firstReviewAtTL == nil || tt.Before(*firstReviewAtTL) {
				firstReviewAtTL = &tt
			}
		case "HeadRefForcePushedEvent":
			tt := t.HeadRefForcePushedEvent.CreatedAt.UTC()
			if firstForcePush == nil || tt.Before(*firstForcePush) {
				firstForcePush = &tt
			}
		case "ReviewRequestedEvent":
			emitReviewRequestRow(t.ReviewRequestedEvent, prNum, slug, sink, prov)
		}
	}
	forcePushedAfterReview = firstForcePush != nil && firstReviewAtTL != nil && firstForcePush.After(*firstReviewAtTL)
	return readyForReviewAt, firstReviewAtTL, forcePushedAfterReview
}

// emitReviewRequestRow writes one pr_review_requests row from a single
// ReviewRequestedEvent node. Empty handle (reviewer deleted; GitHub returns
// null) is silently skipped.
func emitReviewRequestRow(ev reviewRequestedEvent, prNum int, slug string, sink connector.Sink, prov *connector.Provenance) {
	handle, typ := requestedIdentity(ev)
	if handle == "" {
		return
	}
	row := model.PRReviewRequest{
		PRNumber:        prNum,
		Repo:            slug,
		RequestedHandle: handle,
		RequestedType:   typ,
		RequestedAt:     ev.CreatedAt.UTC(),
	}
	if err := sink.InsertPRReviewRequest(row); err != nil {
		if prov.Errors["pr_review_requests"] == "" {
			prov.Errors["pr_review_requests"] = err.Error()
		}
	} else {
		prov.RowsReturned["pr_review_requests"]++
	}
}

// paginatePRReviewRequestsOverflow drains additional REVIEW_REQUESTED_EVENT
// timeline entries for a PR whose inline TimelineItems.PageInfo.HasNextPage
// was true. Only emits pr_review_requests rows; the PR's derived fields
// (ready_for_review_at, first_review_at, force_pushed_after_review) are
// already settled from the inline first-100 timeline.
func (c *Connector) paginatePRReviewRequestsOverflow(ctx context.Context, owner, name string, number int, slug, cursor string, sink connector.Sink, prov *connector.Provenance) {
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		var q struct {
			Repository struct {
				PullRequest struct {
					TimelineItems struct {
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage githubv4.Boolean
						}
						Nodes []reviewRequestNode
					} `graphql:"timelineItems(first: 100, after: $after, itemTypes: [REVIEW_REQUESTED_EVENT])"`
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
			if prov.Errors["pr_review_requests"] == "" {
				prov.Errors["pr_review_requests"] = err.Error()
			}
			c.log.Warn("github: graphql review-requests overflow",
				slog.String("repo", slug),
				slog.Int("pr", number),
				slog.String("error", err.Error()),
			)
			return
		}
		for _, n := range q.Repository.PullRequest.TimelineItems.Nodes {
			emitReviewRequestRow(n.ReviewRequestedEvent, number, slug, sink, prov)
		}
		if !bool(q.Repository.PullRequest.TimelineItems.PageInfo.HasNextPage) {
			return
		}
		cursor = string(q.Repository.PullRequest.TimelineItems.PageInfo.EndCursor)
	}
}

// requestedIdentity returns the handle string and "user" / "team" tag for
// a ReviewRequestedEvent. Empty handle if neither a user login nor a team
// slug is populated (the reviewer was deleted, or GitHub returned a null
// reviewer for some other reason).
func requestedIdentity(ev reviewRequestedEvent) (string, string) {
	if l := string(ev.RequestedReviewer.User.Login); l != "" {
		return l, "user"
	}
	if t := string(ev.RequestedReviewer.Team.CombinedSlug); t != "" {
		return t, "team"
	}
	return "", ""
}
