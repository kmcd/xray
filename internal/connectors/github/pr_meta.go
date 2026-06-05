package github

import (
	"context"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// reviewRequestedEvent matches the GraphQL shape we expect on a
// ReviewRequestedEvent node. Promoted to a named type so the helper
// below can take a real parameter rather than an inline literal.
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

// extractPRReviewRequests walks the PR's timeline for ReviewRequestedEvent
// nodes so that historical requests (since-removed) are captured, not
// just the live request set. ReviewRequestRemovedEvent is intentionally
// excluded; the spec table records the "requested at" moment.
func (c *Connector) extractPRReviewRequests(ctx context.Context, repo connector.Repo, number int, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
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

	cursor := (*githubv4.String)(nil)
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		vars := map[string]any{
			"owner": githubv4.String(owner),
			"name":  githubv4.String(name),
			// #nosec G115 -- PR numbers fit comfortably in int32.
			"number": githubv4.Int(int32(number)),
			"after":  cursor,
		}
		if err := c.gql.Query(ctx, &q, vars); err != nil {
			if prov.Errors["pr_review_requests"] == "" {
				prov.Errors["pr_review_requests"] = err.Error()
			}
			return
		}
		for _, n := range q.Repository.PullRequest.TimelineItems.Nodes {
			ev := n.ReviewRequestedEvent
			handle, typ := requestedIdentity(ev)
			if handle == "" {
				continue
			}
			row := model.PRReviewRequest{
				PRNumber:        number,
				Repo:            repo.Slug,
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
		if !bool(q.Repository.PullRequest.TimelineItems.PageInfo.HasNextPage) {
			return
		}
		end := q.Repository.PullRequest.TimelineItems.PageInfo.EndCursor
		cursor = &end
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
