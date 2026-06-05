package github

import (
	"context"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractPRComments emits both issue and review comments. Bodies are
// measured (length-only) and discarded.
func (c *Connector) extractPRComments(ctx context.Context, repo connector.Repo, number int, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return
	}

	// Issue-style comments (top-level on the PR thread).
	iopts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		comments, resp, err := c.rest.Issues.ListComments(ctx, owner, name, number, iopts)
		if err != nil {
			if prov.Errors["pr_comments"] == "" {
				prov.Errors["pr_comments"] = err.Error()
			}
			break
		}
		for _, cm := range comments {
			handle := cm.GetUser().GetLogin()
			row := model.PRComment{
				PRNumber:     number,
				Repo:         repo.Slug,
				AuthorHandle: handle,
				AuthorIsBot:  isBot(handle),
				CreatedAt:    cm.GetCreatedAt().Time.UTC(),
				Kind:         "issue_comment",
				BodyLength:   len(cm.GetBody()),
			}
			if err := sink.InsertPRComment(row); err != nil {
				if prov.Errors["pr_comments"] == "" {
					prov.Errors["pr_comments"] = err.Error()
				}
			} else {
				prov.RowsReturned["pr_comments"]++
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		iopts.Page = resp.NextPage
	}

	// Review (inline) comments. path is required to enable
	// review-concentrated-on-hotspot correlation.
	ropts := &gh.PullRequestListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		comments, resp, err := c.rest.PullRequests.ListComments(ctx, owner, name, number, ropts)
		if err != nil {
			if prov.Errors["pr_comments"] == "" {
				prov.Errors["pr_comments"] = err.Error()
			}
			return
		}
		for _, cm := range comments {
			handle := cm.GetUser().GetLogin()
			row := model.PRComment{
				PRNumber:     number,
				Repo:         repo.Slug,
				AuthorHandle: handle,
				AuthorIsBot:  isBot(handle),
				CreatedAt:    cm.GetCreatedAt().Time.UTC(),
				Kind:         "review_comment",
				BodyLength:   len(cm.GetBody()),
				Path:         cm.GetPath(),
			}
			if cm.InReplyTo != nil {
				v := *cm.InReplyTo
				row.InReplyTo = &v
			}
			if err := sink.InsertPRComment(row); err != nil {
				if prov.Errors["pr_comments"] == "" {
					prov.Errors["pr_comments"] = err.Error()
				}
			} else {
				prov.RowsReturned["pr_comments"]++
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		ropts.Page = resp.NextPage
	}
}
