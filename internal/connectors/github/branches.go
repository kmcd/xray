package github

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractBranches lists branches from the local clone via `git for-each-ref`
// and, where the token has admin scope, fetches branch protection settings
// via REST. A single 403/404 on the protection endpoint causes the connector
// to mark branch_protection as inaccessible and skip the rest of the
// protection probes for the repo.
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

	protectionAccessible := true
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

		if !protectionAccessible {
			continue
		}
		bp, presp, perr := c.rest.Repositories.GetBranchProtection(ctx, owner, name, b.Name)
		if perr != nil {
			if presp != nil && (presp.StatusCode == http.StatusForbidden || presp.StatusCode == http.StatusNotFound) {
				protectionAccessible = false
				prov.Endpoints["branch_protection"] = connector.EndpointStatus{
					Accessible: false,
					Reason:     "token lacks admin permission on repo",
				}
				continue
			}
			c.log.Warn("github: get branch protection",
				slog.String("repo", repo.Slug),
				slog.String("branch", b.Name),
				slog.String("error", perr.Error()),
			)
			continue
		}
		if bp != nil {
			protRow := buildBranchProtection(repo.Slug, b.Name, bp)
			if err := sink.InsertBranchProtection(protRow); err != nil {
				if prov.Errors["branch_protection"] == "" {
					prov.Errors["branch_protection"] = err.Error()
				}
			} else {
				prov.RowsReturned["branch_protection"]++
			}
		}
	}

	if protectionAccessible {
		prov.Endpoints["branch_protection"] = connector.EndpointStatus{Accessible: true}
	}
}

// buildBranchProtection translates a go-github Protection struct into the
// canonical row shape. Required reviews and check contexts may be nil; we
// preserve null for required_reviews and emit a comma-joined string for
// required_checks.
func buildBranchProtection(repo, branch string, bp *gh.Protection) model.BranchProtection {
	row := model.BranchProtection{Repo: repo, Branch: branch}
	if bp.RequiredPullRequestReviews != nil {
		n := bp.RequiredPullRequestReviews.RequiredApprovingReviewCount
		row.RequiredReviews = &n
	}
	if bp.RequiredStatusChecks != nil {
		ck := bp.RequiredStatusChecks.Checks
		if ck != nil && len(*ck) > 0 {
			names := make([]string, 0, len(*ck))
			for _, c := range *ck {
				if c == nil {
					continue
				}
				names = append(names, c.Context)
			}
			row.RequiredChecks = strings.Join(names, ",")
		} else if ctxs := bp.RequiredStatusChecks.Contexts; ctxs != nil && len(*ctxs) > 0 {
			row.RequiredChecks = strings.Join(*ctxs, ",")
		}
	}
	if bp.EnforceAdmins != nil {
		row.EnforceAdmins = bp.EnforceAdmins.Enabled
	}
	if bp.Restrictions != nil {
		row.RestrictsPushes = true
	}
	return row
}
