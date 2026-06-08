package connector

import (
	"context"
	"time"
)

type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Start) && !t.After(w.End)
}

type Repo struct {
	Slug          string
	DefaultBranch string
	HeadSHA       string
	Team          string
	Clone         string
}

type Connector interface {
	Name() string
	Ping(ctx context.Context) error
	Extract(ctx context.Context, repo Repo, window Window, sink Sink) Provenance
}

type Provenance struct {
	Connector          string                    `json:"connector"`
	Repo               string                    `json:"repo"`
	WindowCovered      Window                    `json:"window_covered"`
	RowsReturned       map[string]int            `json:"rows_returned"`
	PaginationComplete bool                      `json:"pagination_complete"`
	RateLimitTruncated bool                      `json:"rate_limit_truncated"`
	Errors             map[string]string         `json:"errors"`
	Endpoints          map[string]EndpointStatus `json:"endpoints,omitempty"`
	// Flags carries boolean per-extraction signals the manifest aggregates
	// across repos. Currently used for "mailmap_applied". A flag absent from
	// the map reads as false; the aggregator ANDs across all provenances of
	// the same connector to derive the manifest-wide value.
	Flags map[string]bool `json:"flags,omitempty"`

	// GraphQLPointsUsed is the total number of GraphQL rate-limit points
	// consumed across all requests in this extraction. Zero when no GraphQL
	// calls were made (e.g. REST-only connectors).
	GraphQLPointsUsed int `json:"graphql_points_used,omitempty"`
	// GraphQLPointsRemaining is the remaining GitHub GraphQL rate-limit budget
	// as of the last observed response. Zero when no GraphQL calls were made.
	GraphQLPointsRemaining int `json:"graphql_points_remaining,omitempty"`
}

func NewProvenance(name, repo string, w Window) Provenance {
	return Provenance{
		Connector:          name,
		Repo:               repo,
		WindowCovered:      w,
		RowsReturned:       map[string]int{},
		PaginationComplete: true,
		Errors:             map[string]string{},
		Endpoints:          map[string]EndpointStatus{},
		Flags:              map[string]bool{},
	}
}

type EndpointStatus struct {
	Accessible bool   `json:"accessible"`
	Reason     string `json:"reason,omitempty"`
}

// Merge folds other into p in place. Used when a single Extract pass runs
// multiple goroutines that write disjoint provenance fragments (see
// `github.Connector.Extract`'s clone-bound vs API-bound split, #71).
//
// Policy:
//   - RowsReturned counters are summed.
//   - Errors are first-wins per context: p's existing entry sticks, other's
//     fills only previously-empty contexts. Callers organise goroutines so
//     contexts don't collide; on collision the deterministic policy is to
//     keep p's.
//   - PaginationComplete is ANDed.
//   - RateLimitTruncated is ORed.
//   - Endpoints and Flags: other's entry fills if p has none for the key;
//     existing entries on p are preserved.
//
// Window, Connector, Repo are not merged — those are set at NewProvenance
// time and other's values must match. The caller is expected to pass a
// fragment built from the same (connector, repo, window).
func (p *Provenance) Merge(other Provenance) {
	for k, v := range other.RowsReturned {
		p.RowsReturned[k] += v
	}
	for k, v := range other.Errors {
		if _, ok := p.Errors[k]; !ok {
			p.Errors[k] = v
		}
	}
	for k, v := range other.Endpoints {
		if _, ok := p.Endpoints[k]; !ok {
			p.Endpoints[k] = v
		}
	}
	for k, v := range other.Flags {
		if _, ok := p.Flags[k]; !ok {
			p.Flags[k] = v
		}
	}
	if !other.PaginationComplete {
		p.PaginationComplete = false
	}
	if other.RateLimitTruncated {
		p.RateLimitTruncated = true
	}
	p.GraphQLPointsUsed += other.GraphQLPointsUsed
	if other.GraphQLPointsRemaining > 0 {
		if p.GraphQLPointsRemaining == 0 || other.GraphQLPointsRemaining < p.GraphQLPointsRemaining {
			p.GraphQLPointsRemaining = other.GraphQLPointsRemaining
		}
	}
}

// Prefetcher is the optional interface a Connector may implement to allow
// its slow per-repo work to start during the run.go clone phase, in parallel
// with the actual clone. Prefetch must be safe to call concurrently against
// distinct slugs and must stash its result somewhere Extract can find it
// later (typically a per-slug cache held on the connector itself). Extract
// remains the canonical entry point for row emission; Prefetch is purely
// a wall-clock hint. See ADR 022 for the connector-contract baseline and
// the #71 ADR for this extension.
type Prefetcher interface {
	Prefetch(ctx context.Context, slug string, window Window) error
}
