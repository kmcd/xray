package github

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// Default label-sets for issue classification (#184). Operators override via
// connectors.github.issue_bug_labels / issue_regression_labels. Defaults are
// deliberately broad enough to cover the common triage conventions across
// ecosystems (GitHub's "bug", Kubernetes' "kind/bug", scoped "type:bug").
var (
	defaultBugLabels        = []string{"bug", "type:bug", "kind/bug"}
	defaultRegressionLabels = []string{"regression"}
)

// lowerSet builds a lowercased membership set from vals, or from def when
// vals is empty. Labels are trimmed so trailing whitespace in config or API
// payloads does not defeat matching.
func lowerSet(vals, def []string) map[string]bool {
	if len(vals) == 0 {
		vals = def
	}
	out := make(map[string]bool, len(vals))
	for _, v := range vals {
		out[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return out
}

// lowerMap lowercases the keys of m (label → verdict). Returns nil when m is
// empty so callers can treat severity as absent.
func lowerMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

// extractIssues fetches the repo's issues within the window, classifies each
// by its label set, and emits defects (bug-labeled) and incidents
// (regression-labeled, is_regression=1) rows. It is API-bound and runs inside
// goroutine B of Extract, sequentially after extractPRs so the two emitters
// do not race on shared sink batch handles.
//
// No issue title or body text is ever read. Only the issue number,
// open/close timestamps, label names (classified into verdicts), milestone
// title, and author login (bot filter only, never emitted) are touched —
// honouring the no-source-content invariant.
func (c *Connector) extractIssues(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance) {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		prov.Endpoints["issues"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     "invalid slug: " + repo.Slug,
		}
		return
	}

	issues, err := c.fetchIssues(ctx, owner, name, window)
	if err != nil {
		prov.Errors["issues"] = err.Error()
		prov.PaginationComplete = false
		if ctx.Err() != nil {
			// Context cancelled or deadline exceeded mid-walk: truncation, not
			// a permission denial. Leave the endpoint accessible — the
			// analyser reads Accessible=false as "no signal", which is wrong
			// for an interrupted fetch. Mirrors the circleci cancellation
			// handling (commit 8222a84).
			prov.Endpoints["issues"] = connector.EndpointStatus{Accessible: true}
			return
		}
		prov.Endpoints["issues"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
		c.log.Warn("github: list issues",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		return
	}
	prov.Endpoints["issues"] = connector.EndpointStatus{Accessible: true}

	for _, iss := range issues {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		// Bot-filed issues inflate the report count; exclude them
		// consistently with the commit/PR is_bot treatment.
		if isBot(iss.GetUser().GetLogin()) {
			continue
		}

		isBug, isReg, severity := c.classifyIssue(iss)
		if !isBug && !isReg {
			continue
		}

		num := iss.GetNumber()
		numStr := strconv.Itoa(num)
		openedAt := iss.GetCreatedAt().UTC()
		closed := issueClosedAt(iss)

		// An issue may legitimately produce both a defect and an incident;
		// the two tables serve different metrics and the branches are
		// independent.
		if isBug {
			row := model.Defect{
				// scopeID == ref == issue number keeps the 4-part
				// repo:source:scope:ref shape stable and collision-isolated
				// from parsed pr_title/pr_body/commit_message refs.
				ID:        fmt.Sprintf("%s:github_issues:%s:%s", repo.Slug, numStr, numStr),
				Repo:      repo.Slug,
				TicketRef: numStr,
				Source:    "github_issues",
				OpenedAt:  openedAt,
				ClosedAt:  closed,
			}
			if err := sink.InsertDefect(row); err != nil {
				key := "defects:" + numStr
				if prov.Errors[key] == "" {
					prov.Errors[key] = err.Error()
				}
			} else {
				prov.RowsReturned["defects"]++
			}
		}

		if isReg {
			row := model.Incident{
				ID:           numStr,
				Repo:         repo.Slug,
				Source:       "github_issues",
				OpenedAt:     openedAt,
				ResolvedAt:   closed,
				Severity:     severity,
				ReleaseRef:   issueMilestone(iss),
				IsRegression: true,
			}
			if err := sink.InsertIncident(row); err != nil {
				key := "incidents:" + numStr
				if prov.Errors[key] == "" {
					prov.Errors[key] = err.Error()
				}
			} else {
				prov.RowsReturned["incidents"]++
			}
		}
	}
}

// fetchIssues paginates Issues.ListByRepo over [window], skipping pull
// requests (the Issues API returns PRs too) and issues created outside the
// window. Issues are sorted created-ascending so that once a created_at
// passes window.End the walk can stop early. Returns (collected, ctx.Err())
// on cancellation so the caller can treat that as truncation, mirroring
// fetchAllReleases.
func (c *Connector) fetchIssues(ctx context.Context, owner, name string, window connector.Window) ([]*gh.Issue, error) {
	opts := &gh.IssueListByRepoOptions{
		State: "all",
		// Since is updated_at-based, so it is only a coarse lower bound: it
		// can admit older issues recently updated (dropped below by the
		// created_at check) but never excludes an in-window-created issue
		// (updated_at >= created_at >= Since always holds).
		Since:       window.Start,
		Sort:        "created",
		Direction:   "asc",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	var collected []*gh.Issue
	for {
		if ctx.Err() != nil {
			return collected, ctx.Err()
		}
		issues, resp, err := c.rest.Issues.ListByRepo(ctx, owner, name, opts)
		if err != nil {
			return collected, err
		}
		stopPaging := false
		for _, iss := range issues {
			// The Issues API returns pull requests as issues; drop them so
			// PR rows are never double-counted as bug reports.
			if iss.IsPullRequest() {
				continue
			}
			createdAt := iss.GetCreatedAt().UTC()
			// created-ascending: once past window.End the remainder of this
			// page (and every later page) is also out. Stop-paging here is
			// only correct while Sort=created/Direction=asc holds.
			if createdAt.After(window.End) {
				stopPaging = true
				break
			}
			if createdAt.Before(window.Start) {
				continue
			}
			collected = append(collected, iss)
		}
		if stopPaging || resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return collected, nil
}

// classifyIssue inspects the issue's labels against the connector's
// (lowercased) bug / regression sets and severity map. An issue can be both a
// bug and a regression. The first label that hits the severity map wins.
func (c *Connector) classifyIssue(iss *gh.Issue) (isBug, isReg bool, severity string) {
	for _, l := range iss.Labels {
		n := strings.ToLower(strings.TrimSpace(l.GetName()))
		if n == "" {
			continue
		}
		if c.issueBugLabels[n] {
			isBug = true
		}
		if c.issueRegLabels[n] {
			isReg = true
		}
		if severity == "" && c.issueSeverity != nil {
			if s, ok := c.issueSeverity[n]; ok {
				severity = s
			}
		}
	}
	return isBug, isReg, severity
}

// issueClosedAt returns the issue's closed_at as a UTC pointer, or nil while
// the issue is open.
func issueClosedAt(iss *gh.Issue) *time.Time {
	if iss.ClosedAt == nil {
		return nil
	}
	t := iss.ClosedAt.UTC()
	return &t
}

// issueMilestone returns the milestone title (a structural release/version
// marker, e.g. "v2.4.0"), or "" when the issue carries no milestone. The
// title is release metadata, not issue prose, so emitting it does not breach
// the no-source-content invariant.
func issueMilestone(iss *gh.Issue) string {
	if iss.Milestone == nil {
		return ""
	}
	return iss.Milestone.GetTitle()
}
