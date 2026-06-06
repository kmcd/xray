package github

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

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
		Oid githubv4.String
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
	} `graphql:"commits(first: 100)"`
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
	} `graphql:"timelineItems(first: 100, itemTypes: [READY_FOR_REVIEW_EVENT, PULL_REQUEST_REVIEW, HEAD_REF_FORCE_PUSHED_EVENT])"`
	// MergeMethod is on closed/merged PR via merged field; githubv4 doesn't
	// expose it cleanly via PullRequest. We approximate via the merge commit
	// shape post-hoc; see deriveMergeMethod.
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

func (c *Connector) extractPRs(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return
	}

	// Best-effort template fetch up-front; nil result means template_match
	// stays nil on each PR row.
	tpl, err := c.fetchTemplate(ctx, repo.Slug)
	if err != nil {
		c.log.Warn("github: fetch PR template",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
	}

	vars := map[string]any{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
		"first": githubv4.Int(50),
		"after": (*githubv4.String)(nil),
	}

	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		var q prListQuery
		if err := c.gql.Query(ctx, &q, vars); err != nil {
			prov.Errors["prs"] = err.Error()
			c.log.Warn("github: graphql prs",
				slog.String("repo", repo.Slug),
				slog.String("error", err.Error()),
			)
			return
		}
		stopPaging := false
		for _, p := range q.Repository.PullRequests.Nodes {
			created := p.CreatedAt.UTC()
			// PRs ordered by UPDATED_AT desc; the moment we see a PR whose
			// UpdatedAt < window.Start we can stop walking.
			if p.UpdatedAt.Before(window.Start) {
				stopPaging = true
				break
			}
			// Skip PRs that opened after the window end.
			if created.After(window.End) {
				continue
			}
			// Skip PRs that closed before window start and never touched it.
			if p.ClosedAt != nil && p.ClosedAt.Before(window.Start) {
				continue
			}

			c.emitPR(ctx, repo, p, tpl, sink, prov)
		}
		if stopPaging || !bool(q.Repository.PullRequests.PageInfo.HasNextPage) {
			break
		}
		end := q.Repository.PullRequests.PageInfo.EndCursor
		vars["after"] = githubv4.NewString(end)
	}
}

// emitPR writes the PR row + pr_commits + pr_labels for one PR, then hands
// off to reviews / comments / review-requests extractors. firstReviewAt
// is computed from the PR's review listing (REST) so the row reflects it.
func (c *Connector) emitPR(ctx context.Context, repo connector.Repo, p prGraph, tpl *template, sink connector.Sink, prov *connector.Provenance) {
	owner, name, _ := splitSlug(repo.Slug)
	prNum := int(p.Number)

	body := string(p.Body)
	title := string(p.Title)

	// Timeline-derived fields.
	var readyForReviewAt *time.Time
	var firstReviewAtTL *time.Time
	var firstForcePush *time.Time
	for _, t := range p.TimelineItems.Nodes {
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
		}
	}

	forcePushedAfterReview := firstForcePush != nil && firstReviewAtTL != nil && firstForcePush.After(*firstReviewAtTL)

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

	// firstReviewAt is determined more reliably via REST (timeline events
	// include all review events but state values differ on the timeline
	// type). We still seed with the timeline minimum and let the REST pass
	// refine it.
	firstReviewAt := firstReviewAtTL

	// Reviews -> rows + earliest submitted_at.
	if t := c.extractReviews(ctx, repo, prNum, sink, prov); t != nil {
		if firstReviewAt == nil || t.Before(*firstReviewAt) {
			firstReviewAt = t
		}
	}

	// Comments + review requests + labels.
	c.extractPRComments(ctx, repo, prNum, sink, prov)
	c.extractPRReviewRequests(ctx, repo, prNum, sink, prov)
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
		AuthorHandle:           string(p.Author.Login),
		Additions:              int(p.Additions),
		Deletions:              int(p.Deletions),
		FilesChanged:           int(p.ChangedFiles),
		BaseBranch:             string(p.BaseRefName),
		HeadSHA:                string(p.HeadRefOid),
		MergeSHA:               string(p.MergeCommit.Oid),
		MergeMethod:            c.fetchMergeMethod(ctx, owner, name, prNum, repo.Clone, prHeadOids(p)),
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
	// the defects table semantics in CLAUDE.md. Body is parsed here
	// before the local goes out of scope, per the no-raw-bodies rule.
	scopeID := strconv.Itoa(prNum)
	emitDefects(sink, repo.Slug, "pr_title", scopeID, title, row.OpenedAt, row.MergedAt, prov)
	emitDefects(sink, repo.Slug, "pr_body", scopeID, body, row.OpenedAt, row.MergedAt, prov)
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

// fetchMergeMethod returns "merge" / "squash" / "rebase" or empty. Wraps
// deriveMergeMethod with the network and git lookups required to populate
// its inputs.
//
//   - Parent count comes from the REST commit lookup against the merge SHA.
//   - Reachability per PR head commit comes from `git merge-base
//     --is-ancestor` against the per-run clone; when no clone is available
//     (clonePath == "") the function falls back to the historical
//     parent-count-only heuristic (1 parent -> "squash") so behaviour is
//     defined in test-only paths that exercise the connector without a
//     working tree.
func (c *Connector) fetchMergeMethod(ctx context.Context, owner, name string, number int, clonePath string, prHeadCommits []string) string {
	pr, _, err := c.rest.PullRequests.Get(ctx, owner, name, number)
	if err != nil || pr == nil {
		return ""
	}
	if !pr.GetMerged() {
		return ""
	}
	mergeSHA := pr.GetMergeCommitSHA()
	if mergeSHA == "" {
		// No merge commit recorded — treat as rebase per ADR 021's
		// 1-parent + reachable branch (every PR head commit lands as-is).
		return "rebase"
	}
	rc, _, err := c.rest.Repositories.GetCommit(ctx, owner, name, mergeSHA, nil)
	if err != nil || rc == nil {
		return ""
	}
	parents := len(rc.Parents)

	reachable := map[string]bool{}
	if clonePath != "" && c.git != nil {
		for _, oid := range prHeadCommits {
			if oid == "" {
				continue
			}
			ok, ierr := c.git.IsAncestor(ctx, clonePath, oid, mergeSHA)
			if ierr != nil {
				// Treat lookup failures as not-reachable; the squash branch
				// is the safer classification when we cannot confirm.
				reachable[oid] = false
				continue
			}
			reachable[oid] = ok
		}
	} else if parents == 1 {
		// No clone available: fall back to the historical parent-count
		// heuristic. 1 parent -> "squash"; we cannot distinguish rebase.
		return "squash"
	}
	return deriveMergeMethod(parents, prHeadCommits, reachable)
}
