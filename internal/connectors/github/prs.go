package github

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// prListQuery pages PRs ordered by updated-at descending so the connector
// can stop early once it leaves the window.
type prListQuery struct {
	Repository struct {
		PullRequests struct {
			PageInfo struct {
				EndCursor   githubv4.String
				HasNextPage githubv4.Boolean
			}
			Nodes []prGraph
		} `graphql:"pullRequests(first: $first, after: $after, orderBy: {field: UPDATED_AT, direction: DESC})"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type prGraph struct {
	Number       githubv4.Int
	Title        githubv4.String
	Body         githubv4.String
	CreatedAt    githubv4.DateTime
	MergedAt     *githubv4.DateTime
	ClosedAt     *githubv4.DateTime
	UpdatedAt    githubv4.DateTime
	IsDraft      githubv4.Boolean
	Additions    githubv4.Int
	Deletions    githubv4.Int
	ChangedFiles githubv4.Int
	BaseRefName  githubv4.String
	HeadRefName  githubv4.String
	MergeCommit  struct {
		Oid     githubv4.String
		Parents struct {
			TotalCount githubv4.Int
		} `graphql:"parents(first: 5)"`
	}
	HeadRefOid githubv4.String
	Author     struct {
		Login githubv4.String
	}
	HeadRepository struct {
		NameWithOwner githubv4.String
	}
	Commits struct {
		TotalCount githubv4.Int
		PageInfo   struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []struct {
			Commit struct {
				Oid githubv4.String
			}
		}
	} `graphql:"commits(first: 25)"`
	Labels struct {
		Nodes []struct {
			Name githubv4.String
		}
	} `graphql:"labels(first: 50)"`
	ClosingIssuesReferences struct {
		TotalCount githubv4.Int
	} `graphql:"closingIssuesReferences(first: 1)"`
	TimelineItems struct {
		PageInfo struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []timelineNode
	} `graphql:"timelineItems(first: 25, itemTypes: [READY_FOR_REVIEW_EVENT, PULL_REQUEST_REVIEW, HEAD_REF_FORCE_PUSHED_EVENT, REVIEW_REQUESTED_EVENT])"`
	Reviews struct {
		TotalCount githubv4.Int
		PageInfo   struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []reviewGraph
	} `graphql:"reviews(first: 25)"`
	Comments struct {
		TotalCount githubv4.Int
		PageInfo   struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []issueCommentGraph
	} `graphql:"comments(first: 25)"`
	ReviewThreads struct {
		TotalCount githubv4.Int
		PageInfo   struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []reviewThreadGraph
	} `graphql:"reviewThreads(first: 25)"`
}

type timelineNode struct {
	Typename            githubv4.String `graphql:"__typename"`
	ReadyForReviewEvent struct {
		CreatedAt githubv4.DateTime
	} `graphql:"... on ReadyForReviewEvent"`
	PullRequestReview struct {
		CreatedAt githubv4.DateTime
		State     githubv4.String
	} `graphql:"... on PullRequestReview"`
	HeadRefForcePushedEvent struct {
		CreatedAt githubv4.DateTime
	} `graphql:"... on HeadRefForcePushedEvent"`
	ReviewRequestedEvent reviewRequestedEvent `graphql:"... on ReviewRequestedEvent"`
}

// reviewGraph is one PullRequestReview node returned inline on the PR.
// SubmittedAt is nullable because PENDING reviews have not been submitted.
type reviewGraph struct {
	State       githubv4.String
	SubmittedAt *githubv4.DateTime
	Body        githubv4.String
	Author      struct {
		Login githubv4.String
	}
}

// issueCommentGraph is one top-level IssueComment on the PR thread.
type issueCommentGraph struct {
	Author struct {
		Login githubv4.String
	}
	CreatedAt githubv4.DateTime
	Body      githubv4.String
}

// reviewThreadGraph is one PullRequestReviewThread with its inline comments.
//
// Inner comments(first:25) is intentional: GitHub's GraphQL node-count
// limit is 500,000 per query. The bulk prListQuery walks 50 PRs per page
// and the thread×comments product multiplies — at 100 threads × 100
// comments per thread × 50 PRs = 500,000 nodes from this branch alone,
// pushing the total over the limit. 25 comments per thread keeps the bulk
// query well under (the overflow paginator catches threads that exceed it).
type reviewThreadGraph struct {
	Comments struct {
		TotalCount githubv4.Int
		PageInfo   struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []reviewCommentGraph
	} `graphql:"comments(first: 25)"`
}

// reviewCommentGraph is one PullRequestReviewComment node. ReplyTo is nil
// for the thread root.
//
// DatabaseID and ReplyTo.DatabaseID are int64 rather than githubv4.Int
// (int32) because GitHub's REST-side comment IDs have exceeded the int32
// range — observed in the wild at 3,372,977,585 on posthog. shurcooL's
// query builder uses field tags for variable typing only; result-field
// types are honoured by encoding/json directly, so int64 here is safe.
type reviewCommentGraph struct {
	Author struct {
		Login githubv4.String
	}
	CreatedAt  githubv4.DateTime
	Body       githubv4.String
	Path       githubv4.String
	DatabaseID int64 `graphql:"databaseId"`
	ReplyTo    *struct {
		DatabaseID int64 `graphql:"databaseId"`
	}
}

// issueRefRe matches Jira-style ticket prefixes and #N issue references.
// Used to count issue_refs in PR titles and bodies (closing references are
// added on top).
var issueRefRe = regexp.MustCompile(`(?:\b[A-Z][A-Z0-9]+-\d+\b)|(?:#\d+\b)`)

// codeFenceRe counts triple-backtick fences; divide by 2 for blocks.
var codeFenceRe = regexp.MustCompile("(?m)^```")

// markdownLinkRe matches `[text](url)`. imageRe matches `![alt](url)`.
// bareURLRe matches bare http(s) URLs not already wrapped in markdown.
var (
	markdownLinkRe = regexp.MustCompile(`\[[^\]]+\]\([^)]+\)`)
	imageRe        = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
	bareURLRe      = regexp.MustCompile(`https?://[^\s)>\]]+`)
)

// extractPRs is the PR-stage entry point called by Extract. It prefers a
// prefetch cache hit (populated by Prefetch during the run.go clone phase,
// #71) and falls back to a live fetch when no cached nodes are available.
// Emission of rows runs unconditionally.
func (c *Connector) extractPRs(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	nodes, cached, err := c.consumePRPrefetch(ctx, repo.Slug)
	if !cached {
		nodes, err = c.fetchPRs(ctx, repo, window)
	}
	if err != nil {
		prov.Errors["prs"] = err.Error()
		c.log.Warn("github: graphql prs",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		// Continue to emission: any nodes collected before the error are
		// still emit-worthy. fetchPRs returns whatever it gathered up to
		// the point of failure.
	}
	if ctx.Err() != nil {
		prov.PaginationComplete = false
		return
	}
	c.emitPRs(ctx, repo, nodes, sink, prov)
}

// fetchPRs walks prListQuery pages and returns in-window PR nodes. No row
// emission happens here. Same window-filter rules as before: stop paging
// when UpdatedAt < window.Start (PRs are ordered UPDATED_AT desc); skip
// PRs that opened after window.End; skip PRs that closed before window.Start.
// Returns the partial collection plus the first GraphQL error on failure
// so callers can emit whatever did come back.
func (c *Connector) fetchPRs(ctx context.Context, repo connector.Repo, window connector.Window) ([]prGraph, error) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return nil, nil
	}

	vars := map[string]any{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
		"first": githubv4.Int(50),
		"after": (*githubv4.String)(nil),
	}

	var nodes []prGraph
	for {
		if ctx.Err() != nil {
			return nodes, ctx.Err()
		}
		var q prListQuery
		if err := c.gql.Query(ctx, &q, vars); err != nil {
			return nodes, err
		}
		stopPaging := false
		for _, p := range q.Repository.PullRequests.Nodes {
			created := p.CreatedAt.UTC()
			if p.UpdatedAt.Before(window.Start) {
				stopPaging = true
				break
			}
			if created.After(window.End) {
				continue
			}
			if p.ClosedAt != nil && p.ClosedAt.Before(window.Start) {
				continue
			}
			nodes = append(nodes, p)
		}
		if stopPaging || !bool(q.Repository.PullRequests.PageInfo.HasNextPage) {
			break
		}
		end := q.Repository.PullRequests.PageInfo.EndCursor
		vars["after"] = githubv4.NewString(end)
	}
	return nodes, nil
}

// emitPRs walks the supplied prGraph nodes and emits all PR-side rows
// (prs, pr_commits, pr_labels, reviews, pr_comments, pr_review_requests).
// Best-effort template fetch happens here because the template only feeds
// the per-row emit; lifting it into fetchPRs would burn an extra REST
// round-trip during the prefetch path with no upside.
func (c *Connector) emitPRs(ctx context.Context, repo connector.Repo, nodes []prGraph, sink connector.Sink, prov *connector.Provenance) {
	tpl, err := c.fetchTemplate(ctx, repo.Slug)
	if err != nil {
		c.log.Warn("github: fetch PR template",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
	}
	prog := newProgress(c.log, repo.Slug, "prs")
	defer prog.done()
	for _, p := range nodes {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		c.emitPR(ctx, repo, p, tpl, sink, prov)
		prog.tick()
	}
}

// emitPR writes the PR row + pr_commits + pr_labels for one PR. Reviews,
// comments, and review-requests are emitted from the inline GraphQL data
// included in prListQuery. Overflow paginators fire only when an inner
// connection's pageInfo.HasNextPage is true.
func (c *Connector) emitPR(ctx context.Context, repo connector.Repo, p prGraph, tpl *template, sink connector.Sink, prov *connector.Provenance) {
	owner, name, _ := splitSlug(repo.Slug)
	prNum := int(p.Number)

	body := string(p.Body)
	title := string(p.Title)

	// Timeline-derived fields. Also emits pr_review_requests rows from any
	// REVIEW_REQUESTED_EVENT entries in the inline first-100 timeline.
	readyForReviewAt, firstReviewAtTL, forcePushedAfterReview := emitTimelineDerived(p.TimelineItems.Nodes, prNum, repo.Slug, sink, prov)

	// Body shape metrics.
	checklistTotal := strings.Count(body, "- [ ]") + strings.Count(body, "- [x]") + strings.Count(body, "- [X]")
	checklistChecked := strings.Count(body, "- [x]") + strings.Count(body, "- [X]")
	codeBlocks := codeFenceRe.FindAllStringIndex(body, -1)
	codeBlockCount := len(codeBlocks) / 2
	imageCount := len(imageRe.FindAllString(body, -1))

	// Link count: markdown links plus bare URLs that didn't get caught by
	// the markdown matcher. Images use the `![...](...)` form so subtract
	// them from the markdown count to avoid double-counting.
	mdLinks := markdownLinkRe.FindAllString(body, -1)
	bareLinks := bareURLRe.FindAllString(body, -1)
	mdLinkCount := len(mdLinks) - imageCount
	if mdLinkCount < 0 {
		mdLinkCount = 0
	}
	// Subtract any bare URL that already appears inside a markdown link.
	dedupedBare := 0
	for _, b := range bareLinks {
		inMD := false
		for _, m := range mdLinks {
			if strings.Contains(m, b) {
				inMD = true
				break
			}
		}
		if !inMD {
			dedupedBare++
		}
	}
	linkCount := mdLinkCount + dedupedBare

	// Issue refs: title + body matches plus closingIssuesReferences total.
	issueRefs := len(issueRefRe.FindAllString(title, -1)) + len(issueRefRe.FindAllString(body, -1)) + int(p.ClosingIssuesReferences.TotalCount)

	hasRiskMarker := hotfixRe.MatchString(body)
	var tmplScore *float64
	if tpl != nil {
		s := tpl.score(body)
		tmplScore = &s
	}

	// firstReviewAt is seeded from the timeline minimum and refined by the
	// inline review nodes (which carry submittedAt directly).
	firstReviewAt := firstReviewAtTL

	// Reviews from the inline GraphQL connection.
	if t := emitReviewsInline(p.Reviews.Nodes, prNum, repo.Slug, sink, prov); t != nil {
		if firstReviewAt == nil || t.Before(*firstReviewAt) {
			firstReviewAt = t
		}
	}
	if bool(p.Reviews.PageInfo.HasNextPage) {
		if t := c.paginatePRReviewsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.Reviews.PageInfo.EndCursor), sink, prov); t != nil {
			if firstReviewAt == nil || t.Before(*firstReviewAt) {
				firstReviewAt = t
			}
		}
	}

	// PR comments: issue-style top-level and review-thread inline.
	emitIssueCommentsInline(p.Comments.Nodes, prNum, repo.Slug, sink, prov)
	if bool(p.Comments.PageInfo.HasNextPage) {
		c.paginatePRIssueCommentsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.Comments.PageInfo.EndCursor), sink, prov)
	}
	emitReviewThreadsInline(p.ReviewThreads.Nodes, prNum, repo.Slug, sink, prov)
	if bool(p.ReviewThreads.PageInfo.HasNextPage) {
		c.paginatePRReviewThreadsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.ReviewThreads.PageInfo.EndCursor), sink, prov)
	}

	// Timeline overflow picks up any REVIEW_REQUESTED_EVENT past the inline
	// first 100; the derived fields (ready_for_review_at, first_review_at,
	// force_pushed_after_review) keep the inline minimum, which is correct
	// because timeline nodes return in chronological order.
	if bool(p.TimelineItems.PageInfo.HasNextPage) {
		c.paginatePRReviewRequestsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.TimelineItems.PageInfo.EndCursor), sink, prov)
	}

	// pr_labels straight from GraphQL nodes; no extra round-trip.
	for _, l := range p.Labels.Nodes {
		row := model.PRLabel{PRNumber: prNum, Repo: repo.Slug, Label: string(l.Name)}
		if err := sink.InsertPRLabel(row); err != nil {
			if prov.Errors["pr_labels"] == "" {
				prov.Errors["pr_labels"] = err.Error()
			}
		} else {
			prov.RowsReturned["pr_labels"]++
		}
	}

	// pr_commits: emit every commit oid attached to the PR.
	for _, n := range p.Commits.Nodes {
		row := model.PRCommit{PRNumber: prNum, Repo: repo.Slug, SHA: string(n.Commit.Oid)}
		if err := sink.InsertPRCommit(row); err != nil {
			if prov.Errors["pr_commits"] == "" {
				prov.Errors["pr_commits"] = err.Error()
			}
		} else {
			prov.RowsReturned["pr_commits"]++
		}
	}
	// Paginate remaining commits if the PR has more than 100.
	if bool(p.Commits.PageInfo.HasNextPage) {
		c.paginatePRCommits(ctx, owner, name, prNum, repo.Slug, string(p.Commits.PageInfo.EndCursor), sink, prov)
	}

	row := model.PR{
		Number:                 prNum,
		Repo:                   repo.Slug,
		Title:                  title,
		OpenedAt:               p.CreatedAt.UTC(),
		AuthorHandle:           hashHandle(canonicalLogin(string(p.Author.Login))),
		Additions:              int(p.Additions),
		Deletions:              int(p.Deletions),
		FilesChanged:           int(p.ChangedFiles),
		BaseBranch:             string(p.BaseRefName),
		HeadSHA:                string(p.HeadRefOid),
		MergeSHA:               string(p.MergeCommit.Oid),
		MergeMethod:            c.resolveMergeMethod(ctx, p, repo.Clone, prHeadOids(p)),
		IsDraft:                bool(p.IsDraft),
		ReadyForReviewAt:       readyForReviewAt,
		FirstReviewAt:          firstReviewAt,
		CommitCount:            int(p.Commits.TotalCount),
		HeadRepo:               string(p.HeadRepository.NameWithOwner),
		ForcePushedAfterReview: forcePushedAfterReview,
		BodyLength:             len(body),
		TemplateMatch:          tmplScore,
		ChecklistTotal:         checklistTotal,
		ChecklistChecked:       checklistChecked,
		HasRiskMarker:          hasRiskMarker,
		CodeBlockCount:         codeBlockCount,
		ImageCount:             imageCount,
		LinkCount:              linkCount,
		IssueRefsCount:         issueRefs,
	}
	if p.MergedAt != nil {
		t := p.MergedAt.UTC()
		row.MergedAt = &t
	}
	if p.ClosedAt != nil {
		t := p.ClosedAt.UTC()
		row.ClosedAt = &t
	}

	if err := sink.InsertPR(row); err != nil {
		if prov.Errors["prs"] == "" {
			prov.Errors["prs"] = err.Error()
		}
	} else {
		prov.RowsReturned["prs"]++
	}

	// Defect emission from PR title and body. opened_at is the PR's
	// opened_at; closed_at is the merge time (nil if not merged) — see
	// the defects table semantics in docs/spec.md. Body is parsed here
	// before the local goes out of scope, per the no-raw-bodies rule.
	// Per ADR 019 a ref appearing in both title and body emits one row
	// with source = "pr_title".
	emitPRDefects(sink, repo.Slug, prNum, title, body, row.OpenedAt, row.MergedAt, prov)
}

// paginatePRCommits drains additional commit pages for PRs with more than
// 100 commits. Best-effort; on error the PR row is still written.
func (c *Connector) paginatePRCommits(ctx context.Context, owner, name string, number int, slug, cursor string, sink connector.Sink, prov *connector.Provenance) {
	for {
		var q struct {
			Repository struct {
				PullRequest struct {
					Commits struct {
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage githubv4.Boolean
						}
						Nodes []struct {
							Commit struct {
								Oid githubv4.String
							}
						}
					} `graphql:"commits(first: 100, after: $after)"`
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
			return
		}
		for _, n := range q.Repository.PullRequest.Commits.Nodes {
			row := model.PRCommit{PRNumber: number, Repo: slug, SHA: string(n.Commit.Oid)}
			if err := sink.InsertPRCommit(row); err != nil {
				if prov.Errors["pr_commits"] == "" {
					prov.Errors["pr_commits"] = err.Error()
				}
			} else {
				prov.RowsReturned["pr_commits"]++
			}
		}
		if !bool(q.Repository.PullRequest.Commits.PageInfo.HasNextPage) {
			return
		}
		cursor = string(q.Repository.PullRequest.Commits.PageInfo.EndCursor)
	}
}

// prHeadOids returns the commit OIDs in p.Commits.Nodes as plain strings,
// for feeding into reachability checks.
func prHeadOids(p prGraph) []string {
	out := make([]string, 0, len(p.Commits.Nodes))
	for _, n := range p.Commits.Nodes {
		out = append(out, string(n.Commit.Oid))
	}
	return out
}

// deriveMergeMethod infers merge_method from the merge commit's parents and
// whether the PR head commits are reachable from the merge commit (ADR 021).
//
//   - 2 parents          -> "merge"
//   - 1 parent + all PR head commits reachable -> "rebase"
//   - 1 parent + at least one not reachable    -> "squash"
//
// reachable[oid] == true means oid is an ancestor of (or equal to) the
// merge commit; the standard test is `git merge-base --is-ancestor <oid>
// <mergeSHA>`. Returns "" when the merge state is unknown (e.g. an unmerged
// PR with no merge commit).
func deriveMergeMethod(mergeParents int, prHeadCommits []string, reachable map[string]bool) string {
	if mergeParents >= 2 {
		return "merge"
	}
	if mergeParents == 1 {
		for _, c := range prHeadCommits {
			if !reachable[c] {
				return "squash"
			}
		}
		return "rebase"
	}
	return ""
}

// resolveMergeMethod returns "merge" / "squash" / "rebase" or empty.
// Parent count and merge SHA come from the inline GraphQL PR node (no REST
// round-trip). Reachability per PR head commit comes from `git merge-base
// --is-ancestor` against the per-run clone; when no clone is available
// (clonePath == "") the function falls back to the historical
// parent-count-only heuristic (1 parent -> "squash") so behaviour is
// defined in test-only paths that exercise the connector without a working
// tree.
func (c *Connector) resolveMergeMethod(ctx context.Context, p prGraph, clonePath string, prHeadCommits []string) string {
	if p.MergedAt == nil {
		return ""
	}
	mergeSHA := string(p.MergeCommit.Oid)
	if mergeSHA == "" {
		// No merge commit recorded — treat as rebase per ADR 021's
		// 1-parent + reachable branch (every PR head commit lands as-is).
		return "rebase"
	}
	parents := int(p.MergeCommit.Parents.TotalCount)

	reachable := map[string]bool{}
	if clonePath != "" && c.git != nil {
		var ierr error
		reachable, ierr = c.git.CheckAncestors(ctx, clonePath, prHeadCommits, mergeSHA)
		if ierr != nil {
			// Treat lookup failures as not-reachable; the squash branch
			// is the safer classification when we cannot confirm.
			reachable = map[string]bool{}
		}
	} else if parents == 1 {
		// No clone available: fall back to the historical parent-count
		// heuristic. 1 parent -> "squash"; we cannot distinguish rebase.
		return "squash"
	}
	return deriveMergeMethod(parents, prHeadCommits, reachable)
}
