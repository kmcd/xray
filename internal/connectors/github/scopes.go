package github

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/kmcd/xray/internal/preflight"
)

// preflightWorkers caps the concurrency of per-repo preflight GraphQL
// probes. The probes are read-only aggregates and we want to keep
// `xray check` quick on multi-repo configs without blowing through
// GraphQL primary-rate budget before the run even starts.
const preflightWorkers = 5

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
// preview. diskUsage, pullRequests.totalCount, createdAt, and commit
// history total are all O(1) GraphQL reads — no pagination involved.
type repoStatQuery struct {
	Repository struct {
		DiskUsage    githubv4.Int
		CreatedAt    githubv4.DateTime
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
	results := make([]preflight.RepoStat, len(repos))
	sem := make(chan struct{}, preflightWorkers)
	var wg sync.WaitGroup
	for i, slug := range repos {
		owner, name, ok := splitSlug(slug)
		if !ok {
			results[i] = preflight.RepoStat{Slug: slug}
			continue
		}
		select {
		case <-ctx.Done():
			out := make([]preflight.RepoStat, 0, len(repos))
			for j := 0; j < i; j++ {
				out = append(out, results[j])
			}
			return out, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, slug, owner, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			var q repoStatQuery
			vars := map[string]any{
				"owner": githubv4.String(owner),
				"name":  githubv4.String(name),
				"since": githubv4.GitTimestamp{Time: since},
				"until": githubv4.GitTimestamp{Time: until},
			}
			if err := c.queryWithEOFRetry(ctx, &q, vars); err != nil {
				results[i] = preflight.RepoStat{Slug: slug}
				return
			}
			results[i] = preflight.RepoStat{
				Slug:         slug,
				DiskUsageKB:  int64(q.Repository.DiskUsage),
				PullRequests: windowAdjustedPRs(int(q.Repository.PullRequests.TotalCount), q.Repository.CreatedAt.Time, since, until),
				Commits:      int(q.Repository.DefaultBranchRef.Target.Commit.History.TotalCount),
			}
		}(i, slug, owner, name)
	}
	wg.Wait()
	out := make([]preflight.RepoStat, 0, len(repos))
	for _, r := range results {
		if r.Slug != "" {
			out = append(out, r)
		}
	}
	return out, nil
}

// windowAdjustedPRs scales an all-time PR count to an estimate for the
// extraction window. GitHub GraphQL has no windowed PR count, so we
// approximate: allTimePRs × windowDays / repoAgeDays. Without this the
// planner over-estimates by 100-1000× on repos with years of history
// (e.g. 8,000 all-time PRs → 215k API calls vs ~1k actual for 8 days).
// Returns allTimePRs unchanged when any time is zero. Caps at allTimePRs,
// floors at 1 when allTimePRs > 0.
func windowAdjustedPRs(allTimePRs int, repoCreatedAt, since, until time.Time) int {
	if allTimePRs == 0 {
		return 0
	}
	if since.IsZero() || until.IsZero() || repoCreatedAt.IsZero() {
		return allTimePRs
	}
	repoAgeDays := int(until.Sub(repoCreatedAt).Hours() / 24)
	if repoAgeDays < 1 {
		repoAgeDays = 1
	}
	windowDays := int(until.Sub(since).Hours() / 24)
	if windowDays < 1 {
		windowDays = 1
	}
	scaled := allTimePRs * windowDays / repoAgeDays
	if scaled < 1 {
		scaled = 1
	}
	if scaled > allTimePRs {
		scaled = allTimePRs
	}
	return scaled
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
	results := make([]preflight.InaccessibleEndpoint, len(repos))
	sem := make(chan struct{}, preflightWorkers)
	var wg sync.WaitGroup
	for i, slug := range repos {
		owner, name, ok := splitSlug(slug)
		if !ok {
			continue
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			out := collectInaccessible(results[:i])
			return out, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, slug, owner, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			var q branchProtectionProbeQuery
			vars := map[string]any{
				"owner": githubv4.String(owner),
				"name":  githubv4.String(name),
			}
			err := c.queryWithEOFRetry(ctx, &q, vars)
			if err == nil || isTransientProbeError(err) {
				return
			}
			results[i] = preflight.InaccessibleEndpoint{
				Repo:     slug,
				Endpoint: "branch_protection",
				Reason:   condenseProbeReason(err.Error()),
			}
		}(i, slug, owner, name)
	}
	wg.Wait()
	return collectInaccessible(results), nil
}

func collectInaccessible(in []preflight.InaccessibleEndpoint) []preflight.InaccessibleEndpoint {
	var out []preflight.InaccessibleEndpoint
	for _, r := range in {
		if r.Repo != "" {
			out = append(out, r)
		}
	}
	return out
}

// isTransientProbeError reports whether a branch_protection probe error
// is network/timeout/EOF/rate-limit rather than a permission denial. We
// only surface permission denials as "inaccessible" — labelling a flaky
// fetch as inaccessible sends customers to re-mint a token they don't
// need to re-mint.
func isTransientProbeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "unexpected eof"),
		strings.Contains(low, "connection reset"),
		strings.Contains(low, "connection refused"),
		strings.Contains(low, "no such host"),
		strings.Contains(low, "i/o timeout"),
		strings.Contains(low, "rate limit"),
		strings.Contains(low, "secondary rate"),
		strings.Contains(low, "502"),
		strings.Contains(low, "503"),
		strings.Contains(low, "504"),
		strings.Contains(low, "something went wrong while executing your query"):
		return true
	}
	return false
}

// condenseProbeReason maps the verbose GraphQL error string into a short
// customer-facing reason. Only called for non-transient probe failures
// (transient ones are filtered by isTransientProbeError before reaching
// here). Falls back to the raw error truncated.
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

