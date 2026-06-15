package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v66/github"

	"github.com/kmcd/xray/internal/connector"
)

// inWindowReleasesJSON returns a single in-window release plus a one-line
// commits-SHA endpoint registration tag. Mirrors the existing
// TestExtractReleases fixture so assertions stay stable.
const inWindowReleasesJSON = `[
	{"tag_name":"v1.0.0","name":"in-window","created_at":"2025-06-15T00:00:00Z","target_commitish":"main","prerelease":false}
]`

// stubReleasesServer returns an httptest server that serves the standard
// in-window release fixture plus the SHA-resolution endpoint, tracking the
// number of /releases hits via the supplied counter.
func stubReleasesServer(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kmcd/foo/releases", func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		_, _ = w.Write([]byte(inWindowReleasesJSON))
	})
	mux.HandleFunc("/repos/kmcd/foo/commits/v1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1111111111111111111111111111111111111111"))
	})
	return httptest.NewServer(mux)
}

// TestPrefetchReleases_Success drives prefetchReleases against a stub
// /releases endpoint and asserts the per-slug stash carries the expected
// nodes and the done channel closes.
func TestPrefetchReleases_Success(t *testing.T) {
	var hits atomic.Int32
	srv := stubReleasesServer(t, &hits)
	defer srv.Close()
	c := newTestConnector(t, srv)

	if err := c.prefetchReleases(context.Background(), "kmcd/foo", standardWindow()); err != nil {
		t.Fatalf("prefetchReleases: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 /releases hit during prefetch, got %d", got)
	}

	c.prefetchReleasesMu.Lock()
	r, ok := c.prefetchReleasesData["kmcd/foo"]
	c.prefetchReleasesMu.Unlock()
	if !ok {
		t.Fatalf("expected prefetchReleasesData[kmcd/foo] to be stashed")
	}
	select {
	case <-r.done:
	default:
		t.Errorf("expected done channel closed after prefetchReleases returns")
	}
	if r.err != nil {
		t.Errorf("expected nil err on success, got %v", r.err)
	}
	if len(r.nodes) != 1 || r.nodes[0].GetTagName() != "v1.0.0" {
		t.Errorf("expected 1 cached release v1.0.0, got %+v", r.nodes)
	}
}

// TestPrefetchReleases_CacheMiss verifies extractReleases falls back to a
// live walk when no prefetch entry exists for the slug. The /releases
// endpoint must be hit once via the live path.
func TestPrefetchReleases_CacheMiss(t *testing.T) {
	var hits atomic.Int32
	srv := stubReleasesServer(t, &hits)
	defer srv.Close()
	c := newTestConnector(t, srv)

	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if got := hits.Load(); got != 1 {
		t.Errorf("cache miss: expected 1 /releases hit from live walk, got %d", got)
	}
	if len(sink.releases) != 1 {
		t.Errorf("expected 1 release row, got %d", len(sink.releases))
	}
	if _, recorded := prov.Errors["releases:prefetch"]; recorded {
		t.Errorf("releases:prefetch should not be set on cache miss; got %q", prov.Errors["releases:prefetch"])
	}
	if ep := prov.Endpoints["releases"]; !ep.Accessible {
		t.Errorf("expected releases endpoint Accessible=true after live walk, got %+v", ep)
	}
}

// TestPrefetchReleases_CacheHitClean pre-stashes a clean prefetch result
// and asserts extractReleases consumes the cache without re-issuing the
// /releases call. The SHA-resolution call is still expected because
// resolveReleaseSHA hits /commits/<tag> on every emission.
func TestPrefetchReleases_CacheHitClean(t *testing.T) {
	var releaseHits atomic.Int32
	srv := stubReleasesServer(t, &releaseHits)
	defer srv.Close()
	c := newTestConnector(t, srv)

	// Pre-stash a result so extractReleases takes the cache-hit branch.
	createdAt := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	cached := &gh.RepositoryRelease{
		TagName:         gh.String("v1.0.0"),
		Name:            gh.String("in-window"),
		CreatedAt:       &gh.Timestamp{Time: createdAt},
		TargetCommitish: gh.String("main"),
		Prerelease:      gh.Bool(false),
	}
	done := make(chan struct{})
	close(done)
	c.prefetchReleasesMu.Lock()
	c.prefetchReleasesData["kmcd/foo"] = &releasePrefetchResult{
		nodes: []*gh.RepositoryRelease{cached},
		done:  done,
	}
	c.prefetchReleasesMu.Unlock()

	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if got := releaseHits.Load(); got != 0 {
		t.Errorf("cache hit clean: expected 0 /releases hits, got %d", got)
	}
	if len(sink.releases) != 1 || sink.releases[0].Tag != "v1.0.0" {
		t.Errorf("expected 1 release emitted from cache, got %+v", sink.releases)
	}
	if len(sink.deploys) != 1 {
		t.Errorf("expected 1 deploy emitted from cache, got %d", len(sink.deploys))
	}
	if _, recorded := prov.Errors["releases:prefetch"]; recorded {
		t.Errorf("releases:prefetch should not be set on clean cache hit; got %q", prov.Errors["releases:prefetch"])
	}
	if prov.RowsReturned["releases"] != 1 {
		t.Errorf("expected RowsReturned[releases]=1, got %d", prov.RowsReturned["releases"])
	}
}

// TestPrefetchReleases_CacheHitError pre-stashes a result whose err is
// non-nil and asserts extractReleases records prov.Errors[releases:prefetch]
// and falls back to a live walk. REST pagination is opaque, so the
// fallback restarts from page 1 (a single /releases hit).
func TestPrefetchReleases_CacheHitError(t *testing.T) {
	var releaseHits atomic.Int32
	srv := stubReleasesServer(t, &releaseHits)
	defer srv.Close()
	c := newTestConnector(t, srv)

	done := make(chan struct{})
	close(done)
	c.prefetchReleasesMu.Lock()
	c.prefetchReleasesData["kmcd/foo"] = &releasePrefetchResult{
		err:  errors.New("prefetch boom"),
		done: done,
	}
	c.prefetchReleasesMu.Unlock()

	sink := &extraSink{}
	prov := connector.NewProvenance(c.Name(), "kmcd/foo", standardWindow())
	c.extractReleases(context.Background(), connector.Repo{Slug: "kmcd/foo"}, standardWindow(), sink, &prov)

	if got := prov.Errors["releases:prefetch"]; got != "prefetch boom" {
		t.Errorf("expected prov.Errors[releases:prefetch]=prefetch boom, got %q", got)
	}
	if got := releaseHits.Load(); got != 1 {
		t.Errorf("cache hit error: expected 1 /releases hit from live fallback, got %d", got)
	}
	if len(sink.releases) != 1 {
		t.Errorf("expected 1 release row from live fallback, got %d", len(sink.releases))
	}
	if ep := prov.Endpoints["releases"]; !ep.Accessible {
		t.Errorf("expected releases endpoint Accessible=true after successful fallback, got %+v", ep)
	}
}

// TestPrefetchReleases_ContextCancel asserts consumeReleasesPrefetch
// returns cleanly when the consumer context is cancelled while waiting
// on the done channel. The result is removed from the map on consumption
// so no goroutine leak persists in prefetchReleasesData.
func TestPrefetchReleases_ContextCancel(t *testing.T) {
	srv := stubReleasesServer(t, nil)
	defer srv.Close()
	c := newTestConnector(t, srv)

	// Stash an in-flight prefetch (done never closes — simulates a
	// long-running walk).
	c.prefetchReleasesMu.Lock()
	c.prefetchReleasesData["kmcd/foo"] = &releasePrefetchResult{
		done: make(chan struct{}),
	}
	c.prefetchReleasesMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so consume picks the ctx.Done() branch

	nodes, cached, err := c.consumeReleasesPrefetch(ctx, "kmcd/foo")
	if !cached {
		t.Errorf("expected cached=true (stashed entry was deleted on consume)")
	}
	if err == nil {
		t.Errorf("expected ctx error on cancelled consume, got nil")
	}
	if nodes != nil {
		t.Errorf("expected nil nodes on cancelled consume, got %d", len(nodes))
	}
	// Verify the result struct was removed from the map (no leak).
	c.prefetchReleasesMu.Lock()
	_, stillStashed := c.prefetchReleasesData["kmcd/foo"]
	c.prefetchReleasesMu.Unlock()
	if stillStashed {
		t.Errorf("expected prefetchReleasesData[kmcd/foo] to be deleted after consume")
	}
}
