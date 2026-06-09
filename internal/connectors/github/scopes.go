package github

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/preflight"
)

// RequiredScopes is the minimal set of OAuth scopes xray exercises against
// GitHub. Any scope on a token that isn't in this set is reported as
// surplus so the customer can right-size the token, but xray issues only
// read calls regardless of what's granted.
var RequiredScopes = []string{"repo", "read:org"}

// ScopeInfo is the result of a single scope-discovery probe. Granted is
// the parsed X-OAuth-Scopes header from a GET /user response; Extra is the
// granted set minus RequiredScopes. Both are sorted.
type ScopeInfo struct {
	Granted []string
	Extra   []string
}

// Scopes performs a single GET /user call and returns the token's granted
// OAuth scopes as reported by the X-OAuth-Scopes response header. The call
// goes through the connector's existing rate-limited transport — no new
// client is built.
//
// Read-only: GET only. The token never leaves the http client; only the
// returned header values are surfaced.
func (c *Connector) Scopes(ctx context.Context) (ScopeInfo, error) {
	_, resp, err := c.rest.Users.Get(ctx, "")
	if err != nil {
		return ScopeInfo{}, err
	}
	if resp == nil || resp.Response == nil {
		return ScopeInfo{}, nil
	}
	raw := resp.Header.Get("X-OAuth-Scopes")
	granted := parseScopes(raw)
	return ScopeInfo{
		Granted: granted,
		Extra:   diffScopes(granted, RequiredScopes),
	}, nil
}

// parseScopes splits the X-OAuth-Scopes header into a sorted, de-duplicated
// slice. The header is a comma-separated list with optional spaces, e.g.
// "repo, read:org, workflow". An empty header returns nil.
func parseScopes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	seen := map[string]bool{}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// diffScopes returns elements of a that are not in b, preserving sort.
func diffScopes(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

// repoStatQuery is the cheap-aggregate per-repo probe used by the cost
// preview. diskUsage, pullRequests.totalCount, and commit history total
// are all O(1) GraphQL reads — no pagination involved.
type repoStatQuery struct {
	Repository struct {
		DiskUsage    githubv4.Int
		PullRequests struct {
			TotalCount githubv4.Int
		} `graphql:"pullRequests(states: [OPEN, CLOSED, MERGED])"`
		DefaultBranchRef struct {
			Target struct {
				Commit struct {
					History struct {
						TotalCount githubv4.Int
					} `graphql:"history(since: $since, until: $until)"`
				} `graphql:"... on Commit"`
			}
		}
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// RepoStats issues one cheap-aggregate GraphQL query per repo
// (diskUsage + totalCount + windowed commit count) and returns the
// per-repo stats the preflight package needs to build a Plan.
// All endpoints are read-only.
//
// A probe failure on a single repo is recorded as an empty stat and
// the walk continues — `xray check` is a hint, not a gate.
func (c *Connector) RepoStats(ctx context.Context, repos []string, since, until time.Time) ([]preflight.RepoStat, error) {
	out := make([]preflight.RepoStat, 0, len(repos))
	for _, slug := range repos {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		owner, name, ok := splitSlug(slug)
		if !ok {
			continue
		}
		var q repoStatQuery
		vars := map[string]any{
			"owner": githubv4.String(owner),
			"name":  githubv4.String(name),
			"since": githubv4.GitTimestamp{Time: since},
			"until": githubv4.GitTimestamp{Time: until},
		}
		if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
			out = append(out, preflight.RepoStat{Slug: slug})
			continue
		}
		out = append(out, preflight.RepoStat{
			Slug:         slug,
			DiskUsageKB:  int64(q.Repository.DiskUsage),
			PullRequests: int(q.Repository.PullRequests.TotalCount),
			Commits:      int(q.Repository.DefaultBranchRef.Target.Commit.History.TotalCount),
		})
	}
	return out, nil
}

// branchProtectionProbeQuery is the smallest possible branch_protection
// probe: ask for one rule. A permissions error materialises as a GraphQL
// error and is reported back as inaccessible.
type branchProtectionProbeQuery struct {
	Repository struct {
		BranchProtectionRules struct {
			TotalCount githubv4.Int
		} `graphql:"branchProtectionRules(first: 1)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// ProbeEndpoints reports any permission-gated GitHub endpoints xray
// touches during a run that aren't accessible to the current token.
// Today this probes branch_protection only — the other admin-gated
// endpoints (org audit log, repo admin) are not yet exercised by xray.
// Add probes here as new endpoints are pulled in.
func (c *Connector) ProbeEndpoints(ctx context.Context, repos []string) ([]preflight.InaccessibleEndpoint, error) {
	var out []preflight.InaccessibleEndpoint
	for _, slug := range repos {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		owner, name, ok := splitSlug(slug)
		if !ok {
			continue
		}
		var q branchProtectionProbeQuery
		vars := map[string]any{
			"owner": githubv4.String(owner),
			"name":  githubv4.String(name),
		}
		if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
			out = append(out, preflight.InaccessibleEndpoint{
				Repo:     slug,
				Endpoint: "branch_protection",
				Reason:   condenseProbeReason(err.Error()),
			})
		}
	}
	return out, nil
}

// condenseProbeReason maps the verbose GraphQL error string into a short
// customer-facing reason. Falls back to the raw error truncated.
func condenseProbeReason(err string) string {
	low := strings.ToLower(err)
	switch {
	case strings.Contains(low, "must have admin") || strings.Contains(low, "admin"):
		return "admin scope required"
	case strings.Contains(low, "saml") || strings.Contains(low, "sso"):
		return "SSO authorization required"
	case strings.Contains(low, "not found"):
		return "endpoint not visible to this token"
	}
	if len(err) > 80 {
		return err[:80]
	}
	return err
}

