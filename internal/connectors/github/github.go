package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/gitcli"
	xrayprogress "github.com/kmcd/xray/internal/progress"
	"github.com/kmcd/xray/internal/ratelimit"
)

// Connector is the github connector. It owns its own HTTP client (wrapped
// with the ratelimit transport), a REST client, and a GraphQL client.
type Connector struct {
	cfg     config.GitHubConn
	log     *slog.Logger
	capture bool // capture_harness_content flag; read by harnessArtifacts.

	httpClient *http.Client
	rest       *gh.Client
	gql        *githubv4.Client
	git        *gitcli.Client
	rl         *ratelimit.Transport

	// graphqlURL is the endpoint enrichCommits POSTs to. Held separately
	// because the batched-alias query is built as a raw string rather than
	// via shurcooL/githubv4's struct-tag interface. Updated by setBaseURL
	// alongside the gql client for tests.
	graphqlURL string

	// per-connector caches that are safe to reuse across repos.
	mu            sync.Mutex
	templateCache map[string]*template // repo slug -> parsed template (nil if absent)

	// gqlMu guards gqlPointsUsed and gqlPointsRemaining, which are
	// incremented by the costInterceptor on every GraphQL response and
	// read+reset at the start/end of each Extract call.
	gqlMu              sync.Mutex
	gqlPointsUsed      int
	gqlPointsRemaining int

	// prefetchMu guards prefetchData. Prefetch is called from run.go's
	// clone phase (one goroutine per repo) and consumePRPrefetch is called
	// from Extract (which runs in the workers pool). The lock window is
	// only around map mutation; the prefetch goroutine writes to the
	// result struct outside the lock and signals via the result's done
	// channel.
	prefetchMu   sync.Mutex
	prefetchData map[string]*prPrefetchResult // slug -> result

	// prefetchReleasesMu guards prefetchReleasesData. Mirrors the PR
	// prefetch lock; held only for map mutation. The prefetch goroutine
	// writes the result struct outside the lock and signals via done.
	// Releases REST hits a different rate-limit bucket from the GraphQL
	// PR prefetch, so the two prefetchers progress in parallel without
	// same-bucket contention.
	prefetchReleasesMu   sync.Mutex
	prefetchReleasesData map[string]*releasePrefetchResult // slug -> result

	extractShards int

	// prWindow, when non-nil, narrows the PR-cluster extraction to a window
	// narrower than the global run window. Nil means use the global window.
	// Set from config.GitHubConn.PRWindow in New().
	prWindow *connector.Window
}

// prPrefetchResult holds the eventually-available output of a single
// Prefetch call. consumePRPrefetch returns nodes once done is closed.
//
// nextCursor is the GraphQL cursor of the page that failed when err is
// non-nil. It lets extractPRs resume the walk live from where Prefetch
// died, so a transient blip past the retry budget doesn't drop the
// unfetched tail. Empty when err is nil (walk completed) or when the
// failure happened before any page was attempted.
type prPrefetchResult struct {
	nodes      []prGraph
	nextCursor string
	err        error
	done       chan struct{}
}

// releasePrefetchResult holds the eventually-available output of the
// releases half of Prefetch. consumeReleasesPrefetch returns nodes once
// done is closed. There is no resume cursor: the GitHub REST releases
// pagination uses opaque Link-header URLs, so an interrupted walk
// restarts from page 1 in the live fallback rather than resuming.
type releasePrefetchResult struct {
	nodes []*gh.RepositoryRelease
	err   error
	done  chan struct{}
}

// gqlLowWaterMark is the throttleStatus.remaining threshold below which the
// costInterceptor triggers proactive pacing on the ratelimit transport,
// mirroring the REST low-water-mark path in ratelimit.Transport.RoundTrip.
// GitHub's hourly GraphQL budget is 5000 points; 500 gives ~10% headroom.
const gqlLowWaterMark = 500

// costInterceptor wraps an http.RoundTripper and calls onCost after every
// GitHub GraphQL response. It reads the response body to extract the
// extensions.cost and extensions.throttleStatus.remaining fields, then
// reassembles a fresh ReadCloser so the githubv4 decoder can still consume
// the body. Non-GraphQL responses are passed through unmodified.
//
// When rl is non-nil, the interceptor also updates the GQL cost-unit budget
// on the transport and triggers proactive pacing when remaining falls below
// gqlLowWaterMark, matching the REST low-water-mark behaviour.
type costInterceptor struct {
	base   http.RoundTripper
	onCost func(cost, remaining int)
	rl     *ratelimit.Transport
}

func (ci *costInterceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := ci.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if !strings.HasSuffix(req.URL.Path, "/graphql") {
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		// Mid-response truncation (server reset, network drop). Returning the
		// partial body would surface as a downstream JSON decoder
		// "unexpected EOF" that nothing retries. Propagate the read error
		// instead so callers (fetchPRs' queryWithEOFRetry, etc.) can retry
		// the same request. RoundTripper contract: response is nil when
		// error is non-nil.
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	// GitHub returns extensions.cost as a nested object, not a plain int.
	var ext struct {
		Extensions struct {
			Cost struct {
				RequestedQueryCost int `json:"requestedQueryCost"`
				ActualQueryCost    int `json:"actualQueryCost"`
				ThrottleStatus     struct {
					Remaining int `json:"remaining"`
				} `json:"throttleStatus"`
			} `json:"cost"`
		} `json:"extensions"`
	}
	if jsonErr := json.Unmarshal(body, &ext); jsonErr == nil && ext.Extensions.Cost.ActualQueryCost > 0 {
		remaining := ext.Extensions.Cost.ThrottleStatus.Remaining
		ci.onCost(ext.Extensions.Cost.ActualQueryCost, remaining)
		// Only act when remaining is present (> 0). A missing throttleStatus
		// JSON field zero-initialises remaining; treating 0 as authoritative
		// would trigger pacing on every response that omits the field.
		if ci.rl != nil && remaining > 0 {
			resetAt, hasReset := gqlResetAt(resp.Header.Get("X-RateLimit-Reset"))
			ci.rl.UpdateGQLBudget(remaining, resetAt, xrayprogress.FromContext(req.Context()))
			// Only pace when the reset header was present. Without it, the
			// fallback is one hour — pacing on an absent header would stall
			// the run for ~1h, mirroring the REST path which skips pacing
			// when X-RateLimit-Reset is absent or unparseable.
			if hasReset && remaining < gqlLowWaterMark {
				ci.rl.SetGQLPacing(resetAt.Add(5 * time.Second))
			}
		}
	}
	return resp, nil
}

// gqlResetAt parses the X-RateLimit-Reset unix-timestamp header. Returns
// (parsedTime, true) on success or (time.Now()+1h, false) when absent or
// unparseable — the caller uses the bool to distinguish "header present"
// from "synthesised fallback", matching the REST pacing guard.
func gqlResetAt(v string) (time.Time, bool) {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), true
	}
	return time.Now().UTC().Add(time.Hour), false
}

// SetCaptureHarnessContent toggles the harness-artifact content-capture
// flag. The constructor accepts only the GitHub connector config; the
// run wiring sets this from the top-level config.CaptureHarnessContent
// before the connector is invoked.
func (c *Connector) SetCaptureHarnessContent(v bool) {
	c.capture = v
}

// SetExtractShards sets the number of concurrent git subprocesses to use
// for the complexity_history and working-tree phases. 0 or 1 means serial
// (default). Resolved by the run wiring via resolveExtractShards.
func (c *Connector) SetExtractShards(n int) {
	c.extractShards = n
}

// Name returns the connector name as recorded in extraction provenance.
func (c *Connector) Name() string { return "github" }

// BudgetSnapshot returns the current rate-limit budget for this connector.
func (c *Connector) BudgetSnapshot() map[string]ratelimit.BudgetState {
	if c.rl == nil {
		return nil
	}
	return c.rl.Snapshot()
}

// New constructs a Connector with the supplied config and logger.
//
// The logger may be nil; a discarding logger is substituted. The returned
// http.Client carries the ratelimit transport so every REST and GraphQL
// call benefits from retry/backoff without per-call wrapping.
func New(cfg config.GitHubConn, log *slog.Logger) (*Connector, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("github: token is required")
	}
	if log == nil {
		log = slog.Default()
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})
	httpClient := oauth2.NewClient(context.Background(), ts)
	// oauth2.NewClient sets an oauth2.Transport whose Base is the default
	// transport. Wrap that base with our retry transport so retries happen
	// after the token has been attached.
	rl := &ratelimit.Transport{Policy: ratelimit.DefaultPolicy(), Log: log}
	if tr, ok := httpClient.Transport.(*oauth2.Transport); ok {
		// oauth2.NewClient leaves tr.Base nil (→ http.DefaultTransport at
		// RoundTrip time). Install our tuned transport explicitly so the
		// idle-conn pool is sized for the worker pool (#161).
		rl.Base = ratelimit.NewHTTPTransport()
		tr.Base = rl
	} else {
		rl.Base = httpClient.Transport
		httpClient.Transport = rl
	}

	c := &Connector{
		cfg:                  cfg,
		log:                  log,
		httpClient:           httpClient,
		graphqlURL:           "https://api.github.com/graphql",
		git:                  &gitcli.Client{Log: log},
		templateCache:        map[string]*template{},
		prefetchData:         map[string]*prPrefetchResult{},
		prefetchReleasesData: map[string]*releasePrefetchResult{},
		rl:                   rl,
	}
	if cfg.PRWindow != nil {
		w := connector.Window{Start: cfg.PRWindow.Start, End: cfg.PRWindow.End}
		c.prWindow = &w
	}

	// Wrap the outermost transport with the costInterceptor so every GraphQL
	// response updates the connector's running point totals. The wrap goes
	// outside oauth2.Transport so it sees the final response after token
	// injection and retry handling. rl is threaded through so the interceptor
	// can trigger GQL low-water-mark pacing and budget tracking.
	httpClient.Transport = &costInterceptor{
		base: httpClient.Transport,
		rl:   rl,
		onCost: func(cost, remaining int) {
			c.gqlMu.Lock()
			c.gqlPointsUsed += cost
			if remaining > 0 {
				if c.gqlPointsRemaining == 0 || remaining < c.gqlPointsRemaining {
					c.gqlPointsRemaining = remaining
				}
			}
			c.gqlMu.Unlock()
		},
	}

	c.rest = gh.NewClient(httpClient)
	c.gql = githubv4.NewClient(httpClient)

	return c, nil
}

// Prefetch fans out the connector's slug-scoped prefetch work and waits
// for every stage to settle. The two stages — PRs (GraphQL) and releases
// (REST) — hit different rate-limit buckets, so they progress in parallel
// without same-bucket contention. The function signature satisfies the
// connector.Prefetcher interface so run.go can invoke it during the clone
// phase without a github-specific import. Errors from the sub-stages are
// folded into a single returned error: prefetch failures degrade to a
// live fetch in Extract regardless, so the caller only needs to know
// whether anything went wrong, not which stage. Safe to call concurrently
// for distinct slugs.
func (c *Connector) Prefetch(ctx context.Context, slug string, window connector.Window) error {
	var wg sync.WaitGroup
	var prErr, relErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		prErr = c.prefetchPRs(ctx, slug, c.effectivePRWindow(window))
	}()
	go func() {
		defer wg.Done()
		relErr = c.prefetchReleases(ctx, slug, window)
	}()
	wg.Wait()
	if prErr != nil {
		return prErr
	}
	return relErr
}

// prefetchPRs starts a paginated PR walk for the supplied slug and stashes
// the result for Extract to consume later. Idempotent per slug — a second
// call with the same slug awaits the existing result instead of restarting.
func (c *Connector) prefetchPRs(ctx context.Context, slug string, window connector.Window) error {
	r := &prPrefetchResult{done: make(chan struct{})}
	c.prefetchMu.Lock()
	if existing, ok := c.prefetchData[slug]; ok {
		// Already prefetching (or prefetched) for this slug; nothing to
		// do — let the existing result stand. This handles double-call
		// safety even though run.go won't trigger it today.
		c.prefetchMu.Unlock()
		<-existing.done
		return existing.err
	}
	c.prefetchData[slug] = r
	c.prefetchMu.Unlock()

	r.nodes, r.nextCursor, r.err = c.fetchPRs(ctx, connector.Repo{Slug: slug}, window, "")
	close(r.done)
	return r.err
}

// prefetchReleases starts a paginated releases walk for the supplied slug
// and stashes the result for extractReleases to consume later. Mirrors
// prefetchPRs (idempotent per slug, done-channel signalling). REST
// pagination is opaque (Link-header driven), so the stash carries nodes
// only; on error the live fallback restarts from page 1 rather than
// resuming.
func (c *Connector) prefetchReleases(ctx context.Context, slug string, window connector.Window) error {
	r := &releasePrefetchResult{done: make(chan struct{})}
	c.prefetchReleasesMu.Lock()
	if existing, ok := c.prefetchReleasesData[slug]; ok {
		c.prefetchReleasesMu.Unlock()
		<-existing.done
		return existing.err
	}
	c.prefetchReleasesData[slug] = r
	c.prefetchReleasesMu.Unlock()

	r.nodes, r.err = c.fetchAllReleases(ctx, slug, window)
	close(r.done)
	return r.err
}

// consumePRPrefetch returns (nodes, nextCursor, cached, err) for the
// prefetch stashed by Prefetch for slug. cached=false means no prefetch
// ran for this slug — caller falls back to a live fetch. nextCursor is
// non-empty when the prefetch errored mid-walk and a live fetch should
// resume from that cursor instead of restarting. The result struct is
// removed from the map on consumption so a subsequent Extract for the
// same slug (re-run with --keep-clones, etc.) hits the live path rather
// than reusing stale nodes.
func (c *Connector) consumePRPrefetch(ctx context.Context, slug string) ([]prGraph, string, bool, error) {
	c.prefetchMu.Lock()
	r, ok := c.prefetchData[slug]
	if ok {
		delete(c.prefetchData, slug)
	}
	c.prefetchMu.Unlock()
	if !ok {
		return nil, "", false, nil
	}
	select {
	case <-r.done:
		return r.nodes, r.nextCursor, true, r.err
	case <-ctx.Done():
		return nil, "", true, ctx.Err()
	}
}

// consumeReleasesPrefetch returns (nodes, cached, err) for the prefetch
// stashed by prefetchReleases for slug. cached=false means no prefetch
// ran for this slug — caller falls back to a live walk. cached=true with
// err != nil means the prefetch errored mid-walk; caller records
// prov.Errors["releases:prefetch"] and runs the live walk from scratch
// (REST pagination is opaque, no resume cursor). The result struct is
// removed from the map on consumption so a subsequent extract for the
// same slug hits the live path rather than reusing stale nodes.
func (c *Connector) consumeReleasesPrefetch(ctx context.Context, slug string) ([]*gh.RepositoryRelease, bool, error) {
	c.prefetchReleasesMu.Lock()
	r, ok := c.prefetchReleasesData[slug]
	if ok {
		delete(c.prefetchReleasesData, slug)
	}
	c.prefetchReleasesMu.Unlock()
	if !ok {
		return nil, false, nil
	}
	select {
	case <-r.done:
		return r.nodes, true, r.err
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
}

// effectivePRWindow returns the window to use for PR-cluster extraction.
// When c.prWindow is set it is returned (possibly with Start clamped to
// global.Start when the operator declared an earlier start). When nil the
// global window is returned unchanged — preserving the pre-#166 behaviour.
func (c *Connector) effectivePRWindow(global connector.Window) connector.Window {
	if c.prWindow == nil {
		return global
	}
	w := *c.prWindow
	if w.Start.Before(global.Start) {
		c.log.Warn("github: pr_window.start predates global window.start; clamping",
			slog.String("pr_window_start", w.Start.Format("2006-01-02")),
			slog.String("window_start", global.Start.Format("2006-01-02")),
		)
		w.Start = global.Start
	}
	return w
}

// setBaseURL retargets the underlying REST and GraphQL clients at the
// supplied origin (e.g. an httptest.NewServer URL). It is intentionally
// unexported and exists solely to enable HTTP-path tests in _test.go files
// in this package to drive the connector against a local fake. Production
// code paths construct clients pointing at api.github.com via New and never
// call this. rawURL must be a complete URL ("http://host:port" or similar)
// without a trailing path — the REST base becomes "<rawURL>/" and the
// GraphQL endpoint becomes "<rawURL>/graphql".
func (c *Connector) setBaseURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("github: empty base URL")
	}
	if !strings.HasSuffix(rawURL, "/") {
		rawURL += "/"
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("github: parse base URL: %w", err)
	}
	c.rest.BaseURL = u
	c.rest.UploadURL = u
	c.graphqlURL = strings.TrimSuffix(rawURL, "/") + "/graphql"
	c.gql = githubv4.NewEnterpriseClient(c.graphqlURL, c.httpClient)
	return nil
}
