package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
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
		} `graphql:"parents(first: 1)"`
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
		PageInfo struct {
			EndCursor   githubv4.String
			HasNextPage githubv4.Boolean
		}
		Nodes []struct {
			Name githubv4.String
		}
	} `graphql:"labels(first: 10)"`
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
// When the cached Prefetch errored mid-walk and stashed a resume cursor,
// extractPRs continues the walk live from that cursor so the unfetched
// tail isn't dropped. Emission of rows runs unconditionally.
func (c *Connector) extractPRs(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	nodes, nextCursor, cached, err := c.consumePRPrefetch(ctx, repo.Slug)
	switch {
	case !cached:
		nodes, _, err = c.fetchPRs(ctx, repo, window, "")
	case err != nil && nextCursor != "" && ctx.Err() == nil:
		// Record the prefetch failure before attempting live resume; the
		// resume may clear err on success but the prefetch interruption is
		// still a real event worth preserving in provenance.
		prov.Errors["prs:prefetch"] = err.Error()
		c.log.Warn("github: prefetch errored mid-walk, resuming live from cursor",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		moreNodes, _, liveErr := c.fetchPRs(ctx, repo, window, nextCursor)
		nodes = append(nodes, moreNodes...)
		err = liveErr
	}
	if err != nil {
		prov.Errors["prs"] = err.Error()
		prov.PaginationComplete = false
		c.log.Warn("github: graphql prs",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		// Continue to emission: any nodes collected before the error are
		// still emit-worthy.
	}
	if ctx.Err() != nil {
		prov.PaginationComplete = false
		return
	}
	c.emitPRs(ctx, repo, nodes, sink, prov)
}

// fetchPRs walks prListQuery pages and returns in-window PR nodes. No row
// emission happens here. Same window-filter rules: stop paging when
// UpdatedAt < window.Start (PRs are ordered UPDATED_AT desc); skip PRs
// that opened after window.End; skip PRs that closed before window.Start.
//
// startCursor is the GraphQL cursor to begin the walk at; empty means
// page 1. Returns (collected nodes, resumeCursor, err): resumeCursor is
// the cursor of the page that failed (so a caller can retry from that
// exact point) and is empty when the walk completed cleanly or hit
// stopPaging. On terminal failure, the caller gets whatever nodes did
// come back; resumeCursor lets `extractPRs` continue the walk live when
// Prefetch errored mid-stream.
func (c *Connector) fetchPRs(ctx context.Context, repo connector.Repo, window connector.Window, startCursor string) ([]prGraph, string, error) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return nil, "", nil
	}

	cursor := startCursor
	vars := map[string]any{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
		"first": githubv4.Int(50),
		"after": (*githubv4.String)(nil),
	}

	var nodes []prGraph
	for {
		if ctx.Err() != nil {
			return nodes, cursor, ctx.Err()
		}
		if cursor == "" {
			vars["after"] = (*githubv4.String)(nil)
		} else {
			vars["after"] = githubv4.NewString(githubv4.String(cursor))
		}
		var q prListQuery
		if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
			return nodes, cursor, err
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
			return nodes, "", nil
		}
		cursor = string(q.Repository.PullRequests.PageInfo.EndCursor)
	}
}

// queryWithEOFRetry runs c.gql.Query with bounded retries when the error is
// a transient network failure: mid-response body truncation
// (io.ErrUnexpectedEOF, io.EOF, "unexpected EOF") or a stale-connection TCP
// reset ("connection reset by peer") that occurs after long idle periods
// such as primary-rate-limit waits. The GraphQL cursor in vars is unchanged
// across attempts — GitHub cursors hold for minutes, longer than our retry
// budget. Non-transient errors return immediately.
//
// The ratelimit.Transport already retries HTTP-level transient errors
// (429/5xx/secondary-RL), but body-read failures and TCP resets inside the
// outer costInterceptor surface as transport errors *above* ratelimit and
// bypass that layer. This helper closes that gap for the PR-list walk.
func (c *Connector) queryWithEOFRetry(ctx context.Context, q any, vars map[string]any) error {
	const maxAttempts = 3
	const budget = 60 * time.Second
	var spent time.Duration
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 500 * time.Millisecond
	bo.MaxInterval = 10 * time.Second
	bo.MaxElapsedTime = 0
	bo.Reset()

	for attempt := 1; ; attempt++ {
		err := c.gql.Query(ctx, q, vars)
		if err == nil {
			return nil
		}
		if attempt >= maxAttempts || !isTransientEOF(err) {
			return err
		}
		wait := bo.NextBackOff()
		if spent+wait > budget {
			return err
		}
		spent += wait
		c.log.Warn("github: gql query transient network error, retrying",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.String("error", err.Error()),
		)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// doJSONPOSTWithEOFRetry POSTs body to url as application/json with bounded
// retries on transient mid-response EOFs. The request is rebuilt each
// attempt from the captured body so retries are body-safe. Used by the
// enrich.go raw-POST path; the gql.Query path uses queryWithEOFRetry.
//
// Callers own response.Body close as usual on a non-nil response.
func (c *Connector) doJSONPOSTWithEOFRetry(ctx context.Context, url string, body []byte) (*http.Response, error) {
	const maxAttempts = 3
	const budget = 60 * time.Second
	var spent time.Duration
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 500 * time.Millisecond
	bo.MaxInterval = 10 * time.Second
	bo.MaxElapsedTime = 0
	bo.Reset()

	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := c.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}
		if attempt >= maxAttempts || !isTransientEOF(err) {
			return nil, err
		}
		wait := bo.NextBackOff()
		if spent+wait > budget {
			return nil, err
		}
		spent += wait
		c.log.Warn("github: http POST transient network error, retrying",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.String("error", err.Error()),
		)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// isTransientEOF reports whether err is a transient network failure that is
// safe to retry with the same GraphQL cursor: body-read truncations
// (io.ErrUnexpectedEOF, io.EOF, "unexpected EOF") and stale-connection TCP
// resets ("connection reset by peer") that occur after long idle periods.
// See isTransientProbeError in scopes.go for the analogous probe-path check.
func isTransientEOF(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset by peer")
}

// emitPRs walks the supplied prGraph nodes and emits all PR-side rows
// (prs, pr_commits, pr_labels, reviews, pr_comments, pr_review_requests).
// Best-effort template fetch happens here because the template only feeds
// the per-row emit; lifting it into fetchPRs would burn an extra REST
// round-trip during the prefetch path with no upside.
//
// Hot-table batches (prs, pr_commits, pr_labels, reviews, pr_comments) are
// opened once at the top and committed once at the bottom — every PR
// contributes to the same five tx-bound buffers, so the per-PR overhead
// of an explicit tx amortises across the whole batch. pr_review_requests
// stays per-row (cold table).
func (c *Connector) emitPRs(ctx context.Context, repo connector.Repo, nodes []prGraph, sink connector.Sink, prov *connector.Provenance) {
	tpl, err := c.fetchTemplate(ctx, repo.Slug, prov)
	if err != nil {
		c.log.Warn("github: fetch PR template",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
	}

	prsB := openPRsBatch(sink)
	defer prsB.Rollback()
	prcB := openPRCommitsBatch(sink)
	defer prcB.Rollback()
	prlB := openPRLabelsBatch(sink)
	defer prlB.Rollback()
	revB := openReviewsBatch(sink)
	defer revB.Rollback()
	cmtB := openPRCommentsBatch(sink)
	defer cmtB.Rollback()

	prog := newProgress(c.log, repo.Slug, "prs")
	defer prog.done()
	for _, p := range nodes {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			break
		}
		c.emitPR(ctx, repo, p, tpl, sink, prsB, prcB, prlB, revB, cmtB, prov)
		prog.tick()
	}

	commitBatch(prsB, prov, "prs")
	commitBatch(prcB, prov, "pr_commits")
	commitBatch(prlB, prov, "pr_labels")
	commitBatch(revB, prov, "reviews")
	commitBatch(cmtB, prov, "pr_comments")
}

// emitPR writes the PR row + pr_commits + pr_labels for one PR. Reviews,
// comments, and review-requests are emitted from the inline GraphQL data
// included in prListQuery. Overflow paginators fire only when an inner
// connection's pageInfo.HasNextPage is true.
//
// The five hot-table batch handles are owned by emitPRs; emitPR adds to them
// and never Commits — the caller flushes after the whole nodes slice walks.
func (c *Connector) emitPR(ctx context.Context, repo connector.Repo, p prGraph, tpl *template, sink connector.Sink, prsB prsBatch, prcB prCommitsBatch, prlB prLabelsBatch, revB reviewsBatch, cmtB prCommentsBatch, prov *connector.Provenance) {
	owner, name, _ := splitSlug(repo.Slug)
	prNum := int(p.Number)

	body := string(p.Body)
	title := string(p.Title)

	// Timeline-derived fields. Also emits pr_review_requests rows from any
	// REVIEW_REQUESTED_EVENT entries in the inline first-25 timeline.
	readyForReviewAt, firstReviewAtTL, firstForcePush := emitTimelineDerived(p.TimelineItems.Nodes, prNum, repo.Slug, sink, prov)

	// Timeline overflow: drain any events past the inline first-25. This
	// runs before body metrics so that firstReviewAtTL and firstForcePush
	// are fully populated before forcePushedAfterReview is computed.
	if bool(p.TimelineItems.PageInfo.HasNextPage) {
		ovReady, ovFirstReview, ovFP := c.paginatePRTimelineOverflow(ctx, owner, name, prNum, repo.Slug, string(p.TimelineItems.PageInfo.EndCursor), sink, prov)
		if ovReady != nil && (readyForReviewAt == nil || ovReady.Before(*readyForReviewAt)) {
			readyForReviewAt = ovReady
		}
		if ovFirstReview != nil && (firstReviewAtTL == nil || ovFirstReview.Before(*firstReviewAtTL)) {
			firstReviewAtTL = ovFirstReview
		}
		if ovFP != nil && (firstForcePush == nil || ovFP.Before(*firstForcePush)) {
			firstForcePush = ovFP
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

	// firstReviewAt is seeded from the timeline minimum and refined by the
	// inline review nodes (which carry submittedAt directly).
	firstReviewAt := firstReviewAtTL

	// Reviews from the inline GraphQL connection.
	if t := emitReviewsInline(p.Reviews.Nodes, prNum, repo.Slug, revB, prov); t != nil {
		if firstReviewAt == nil || t.Before(*firstReviewAt) {
			firstReviewAt = t
		}
	}
	if bool(p.Reviews.PageInfo.HasNextPage) {
		if t := c.paginatePRReviewsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.Reviews.PageInfo.EndCursor), revB, prov); t != nil {
			if firstReviewAt == nil || t.Before(*firstReviewAt) {
				firstReviewAt = t
			}
		}
	}

	// PR comments: issue-style top-level and review-thread inline.
	emitIssueCommentsInline(p.Comments.Nodes, prNum, repo.Slug, cmtB, prov)
	if bool(p.Comments.PageInfo.HasNextPage) {
		c.paginatePRIssueCommentsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.Comments.PageInfo.EndCursor), cmtB, prov)
	}
	emitReviewThreadsInline(p.ReviewThreads.Nodes, prNum, repo.Slug, cmtB, prov)
	if bool(p.ReviewThreads.PageInfo.HasNextPage) {
		c.paginatePRReviewThreadsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.ReviewThreads.PageInfo.EndCursor), cmtB, prov)
	}

	// pr_labels straight from GraphQL nodes; no extra round-trip.
	emitPRLabelsInline(p.Labels.Nodes, prNum, repo.Slug, prlB, prov)
	if bool(p.Labels.PageInfo.HasNextPage) {
		c.paginatePRLabelsOverflow(ctx, owner, name, prNum, repo.Slug, string(p.Labels.PageInfo.EndCursor), prlB, prov)
	}

	// pr_commits: emit every commit oid attached to the PR.
	headOids := prHeadOids(p)
	for _, n := range p.Commits.Nodes {
		row := model.PRCommit{PRNumber: prNum, Repo: repo.Slug, SHA: string(n.Commit.Oid)}
		if err := prcB.Add(row); err != nil {
			if prov.Errors["pr_commits"] == "" {
				prov.Errors["pr_commits"] = err.Error()
			}
		}
	}
	// Paginate remaining commits if the PR has more than 25; collect overflow
	// OIDs so resolveMergeMethod sees the full head-commit set.
	if bool(p.Commits.PageInfo.HasNextPage) {
		overflowOids := c.paginatePRCommits(ctx, owner, name, prNum, repo.Slug, string(p.Commits.PageInfo.EndCursor), prcB, prov)
		headOids = append(headOids, overflowOids...)
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
		MergeMethod:            c.resolveMergeMethod(ctx, p, repo.Clone, headOids),
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

	if err := prsB.Add(row); err != nil {
		if prov.Errors["prs"] == "" {
			prov.Errors["prs"] = err.Error()
		}
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
// 25 commits. Returns the OIDs collected from the overflow pages so the
// caller can pass the full set to resolveMergeMethod. Best-effort; on
// error the rows written so far are kept and the partial OID slice is
// returned.
func (c *Connector) paginatePRCommits(ctx context.Context, owner, name string, number int, slug, cursor string, b prCommitsBatch, prov *connector.Provenance) []string {
	var oids []string
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
		if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
			prov.Errors[fmt.Sprintf("pr_commits:%d", number)] = err.Error()
			prov.PaginationComplete = false
			return oids
		}
		for _, n := range q.Repository.PullRequest.Commits.Nodes {
			oid := string(n.Commit.Oid)
			oids = append(oids, oid)
			row := model.PRCommit{PRNumber: number, Repo: slug, SHA: oid}
			if err := b.Add(row); err != nil {
				if prov.Errors["pr_commits"] == "" {
					prov.Errors["pr_commits"] = err.Error()
				}
			}
		}
		if !bool(q.Repository.PullRequest.Commits.PageInfo.HasNextPage) {
			return oids
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

// emitPRLabelsInline writes pr_labels rows from the inline GraphQL connection
// on a PR.
func emitPRLabelsInline(nodes []struct{ Name githubv4.String }, prNum int, slug string, b prLabelsBatch, prov *connector.Provenance) {
	for _, l := range nodes {
		row := model.PRLabel{PRNumber: prNum, Repo: slug, Label: string(l.Name)}
		if err := b.Add(row); err != nil {
			if prov.Errors["pr_labels"] == "" {
				prov.Errors["pr_labels"] = err.Error()
			}
		}
	}
}

// paginatePRLabelsOverflow drains additional label pages for a PR whose inline
// Labels.PageInfo.HasNextPage was true. Best-effort; on error the rows written
// so far are kept and pagination is marked incomplete in provenance.
func (c *Connector) paginatePRLabelsOverflow(ctx context.Context, owner, name string, number int, slug, cursor string, b prLabelsBatch, prov *connector.Provenance) {
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		var q struct {
			Repository struct {
				PullRequest struct {
					Labels struct {
						PageInfo struct {
							EndCursor   githubv4.String
							HasNextPage githubv4.Boolean
						}
						Nodes []struct {
							Name githubv4.String
						}
					} `graphql:"labels(first: 100, after: $after)"`
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
		if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
			if prov.Errors["pr_labels"] == "" {
				prov.Errors["pr_labels"] = err.Error()
			}
			prov.PaginationComplete = false
			c.log.Warn("github: graphql labels overflow",
				slog.String("repo", slug),
				slog.Int("pr", number),
				slog.String("error", err.Error()),
			)
			return
		}
		emitPRLabelsInline(q.Repository.PullRequest.Labels.Nodes, number, slug, b, prov)
		if !bool(q.Repository.PullRequest.Labels.PageInfo.HasNextPage) {
			return
		}
		cursor = string(q.Repository.PullRequest.Labels.PageInfo.EndCursor)
	}
}
