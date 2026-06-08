package github

import (
	"context"
	"log/slog"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// emitIssueCommentsInline emits issue_comment rows from the PR's inline
// top-level Comments connection. Body strings are length-measured and
// discarded — never persisted.
func emitIssueCommentsInline(nodes []issueCommentGraph, prNum int, slug string, sink connector.Sink, prov *connector.Provenance) {
	for _, cm := range nodes {
		login := string(cm.Author.Login)
		row := model.PRComment{
			PRNumber:     prNum,
			Repo:         slug,
			AuthorHandle: hashHandle(canonicalLogin(login)),
			AuthorIsBot:  isBot(login),
			CreatedAt:    cm.CreatedAt.UTC(),
			Kind:         "issue_comment",
			BodyLength:   len(string(cm.Body)),
		}
		if err := sink.InsertPRComment(row); err != nil {
			if prov.Errors["pr_comments"] == "" {
				prov.Errors["pr_comments"] = err.Error()
			}
		} else {
			prov.RowsReturned["pr_comments"]++
		}
	}
}

// emitReviewThreadsInline emits review_comment rows from the PR's inline
// ReviewThreads connection. Each thread contributes one or more comments;
// the thread root has a nil replyTo, replies carry the parent's databaseId.
func emitReviewThreadsInline(threads []reviewThreadGraph, prNum int, slug string, sink connector.Sink, prov *connector.Provenance) {
	for _, th := range threads {
		emitReviewCommentNodes(th.Comments.Nodes, prNum, slug, sink, prov)
	}
}

// emitReviewCommentNodes writes review-comment rows from a slice of
// PullRequestReviewComment GraphQL nodes.
func emitReviewCommentNodes(nodes []reviewCommentGraph, prNum int, slug string, sink connector.Sink, prov *connector.Provenance) {
	for _, cm := range nodes {
		login := string(cm.Author.Login)
		row := model.PRComment{
			PRNumber:     prNum,
			Repo:         slug,
			AuthorHandle: hashHandle(canonicalLogin(login)),
			AuthorIsBot:  isBot(login),
			CreatedAt:    cm.CreatedAt.UTC(),
			Kind:         "review_comment",
			BodyLength:   len(string(cm.Body)),
			Path:         string(cm.Path),
		}
		if cm.ReplyTo != nil {
			v := cm.ReplyTo.DatabaseID
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
}

// paginatePRIssueCommentsOverflow drains additional issue-style comment
// pages for a PR whose inline Comments.PageInfo.HasNextPage was true.
func (c *Connector) paginatePRIssueCommentsOverflow(ctx context.Context, owner, name string, number int, slug, cursor string, sink connector.Sink, prov *connector.Provenance) {
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		var q struct {
			Repository struct {
				PullRequest struct {
					Comments struct {
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage githubv4.Boolean
						}
						Nodes []issueCommentGraph
					} `graphql:"comments(first: 100, after: $after)"`
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
			if prov.Errors["pr_comments"] == "" {
				prov.Errors["pr_comments"] = err.Error()
			}
			c.log.Warn("github: graphql issue-comments overflow",
				slog.String("repo", slug),
				slog.Int("pr", number),
				slog.String("error", err.Error()),
			)
			return
		}
		emitIssueCommentsInline(q.Repository.PullRequest.Comments.Nodes, number, slug, sink, prov)
		if !bool(q.Repository.PullRequest.Comments.PageInfo.HasNextPage) {
			return
		}
		cursor = string(q.Repository.PullRequest.Comments.PageInfo.EndCursor)
	}
}

// paginatePRReviewThreadsOverflow drains additional review-thread pages
// for a PR whose inline ReviewThreads.PageInfo.HasNextPage was true.
// Each thread's inner Comments connection is also walked at first=100;
// threads with >100 inline comments are recovered via the per-thread
// pagination loop in paginateReviewThreadComments.
func (c *Connector) paginatePRReviewThreadsOverflow(ctx context.Context, owner, name string, number int, slug, cursor string, sink connector.Sink, prov *connector.Provenance) {
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		var q struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage githubv4.Boolean
						}
						Nodes []reviewThreadGraph
					} `graphql:"reviewThreads(first: 100, after: $after)"`
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
			if prov.Errors["pr_comments"] == "" {
				prov.Errors["pr_comments"] = err.Error()
			}
			c.log.Warn("github: graphql review-threads overflow",
				slog.String("repo", slug),
				slog.Int("pr", number),
				slog.String("error", err.Error()),
			)
			return
		}
		emitReviewThreadsInline(q.Repository.PullRequest.ReviewThreads.Nodes, number, slug, sink, prov)
		if !bool(q.Repository.PullRequest.ReviewThreads.PageInfo.HasNextPage) {
			return
		}
		cursor = string(q.Repository.PullRequest.ReviewThreads.PageInfo.EndCursor)
	}
}
