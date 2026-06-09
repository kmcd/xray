package github

import (
	"context"
	"log/slog"
	"strings"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractBranches lists branches from the local clone via `git for-each-ref`
// and fetches branch protection rules via a single paginated GraphQL query
// against repository.branchProtectionRules. A GraphQL error (e.g. token lacks
// admin permission) marks branch_protection inaccessible and skips rows for
// that endpoint.
func (c *Connector) extractBranches(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) {
	if repo.Clone == "" || c.git == nil {
		return
	}
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return
	}

	branches, err := c.git.RemoteBranches(ctx, repo.Clone)
	if err != nil {
		prov.Errors["branches"] = err.Error()
		c.log.Warn("github: list branches",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		return
	}

	for _, b := range branches {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		row := model.Branch{
			Repo:          repo.Slug,
			Name:          b.Name,
			LastCommitSHA: b.LastCommitSHA,
			LastCommitAt:  b.LastCommitAt,
			IsDefault:     strings.EqualFold(b.Name, repo.DefaultBranch),
		}
		if err := sink.InsertBranch(row); err != nil {
			if prov.Errors["branches"] == "" {
				prov.Errors["branches"] = err.Error()
			}
		} else {
			prov.RowsReturned["branches"]++
		}
	}

	c.fetchBranchProtectionRules(ctx, owner, name, sink, prov)
}

type branchProtectionQuery struct {
	Repository struct {
		BranchProtectionRules struct {
			PageInfo struct {
				EndCursor   githubv4.String
				HasNextPage githubv4.Boolean
			}
			Nodes []branchProtectionRuleGraph
		} `graphql:"branchProtectionRules(first: 100, after: $after)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type branchProtectionRuleGraph struct {
	RequiresApprovingReviews     githubv4.Boolean
	RequiredApprovingReviewCount githubv4.Int
	IsAdminEnforced              githubv4.Boolean
	RestrictsPushes              githubv4.Boolean
	RequiredStatusCheckContexts  []githubv4.String
	MatchingRefs                 struct {
		Nodes []struct {
			Name githubv4.String
		}
	} `graphql:"matchingRefs(first: 100)"`
}

// fetchBranchProtectionRules fetches all branch protection rules for the repo
// in one paginated GraphQL call and fans out one branch_protection row per
// matching branch per rule.
func (c *Connector) fetchBranchProtectionRules(ctx context.Context, owner, name string, sink connector.Sink, prov *connector.Provenance) {
	vars := map[string]any{
		"owner": githubv4.String(owner),
		"name":  githubv4.String(name),
		"after": (*githubv4.String)(nil),
	}
	repoSlug := owner + "/" + name
	for {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			return
		}
		var q branchProtectionQuery
		if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
			prov.Endpoints["branch_protection"] = connector.EndpointStatus{
				Accessible: false,
				Reason:     err.Error(),
			}
			return
		}
		for _, rule := range q.Repository.BranchProtectionRules.Nodes {
			for _, ref := range rule.MatchingRefs.Nodes {
				row := buildBranchProtectionFromRule(repoSlug, string(ref.Name), rule)
				if err := sink.InsertBranchProtection(row); err != nil {
					if prov.Errors["branch_protection"] == "" {
						prov.Errors["branch_protection"] = err.Error()
					}
				} else {
					prov.RowsReturned["branch_protection"]++
				}
			}
		}
		if !bool(q.Repository.BranchProtectionRules.PageInfo.HasNextPage) {
			break
		}
		vars["after"] = githubv4.NewString(q.Repository.BranchProtectionRules.PageInfo.EndCursor)
	}
	prov.Endpoints["branch_protection"] = connector.EndpointStatus{Accessible: true}
}

// buildBranchProtectionFromRule translates a GraphQL BranchProtectionRule node
// and a matched branch name into the canonical row shape.
func buildBranchProtectionFromRule(repo, branch string, r branchProtectionRuleGraph) model.BranchProtection {
	row := model.BranchProtection{Repo: repo, Branch: branch}
	if bool(r.RequiresApprovingReviews) {
		n := int(r.RequiredApprovingReviewCount)
		row.RequiredReviews = &n
	}
	if len(r.RequiredStatusCheckContexts) > 0 {
		names := make([]string, len(r.RequiredStatusCheckContexts))
		for i, ctx := range r.RequiredStatusCheckContexts {
			names[i] = string(ctx)
		}
		row.RequiredChecks = strings.Join(names, ",")
	}
	row.EnforceAdmins = bool(r.IsAdminEnforced)
	row.RestrictsPushes = bool(r.RestrictsPushes)
	return row
}
