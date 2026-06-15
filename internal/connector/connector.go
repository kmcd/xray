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

	// ConfigDepth records operator-declared extraction-depth overrides that
	// narrow what data was captured. Absent keys mean the connector ran at
	// full depth. The analyser reads this to interpret reduced row counts as
	// "out of scope" rather than "no signal". Currently used by the github
	// connector for "pr_window" and "pr_history_sample".
	ConfigDepth map[string]string `json:"config_depth,omitempty"`

	// Sampling holds per-bucket statistics when sparse-historical PR sampling
	// is active (pr_inflection + pr_history_sample configured). Nil when the
	// connector ran at full PR fidelity. The analyser uses the per-bucket
	// target/actual/total counts to compute confidence intervals on metrics
	// derived from pre-bracket sparse data.
	Sampling *SamplingProvenance `json:"sampling,omitempty"`
}

// SamplingProvenance records the sparse-historical PR sampling configuration
// and per-bucket extraction results. Present only when pr_inflection is set.
type SamplingProvenance struct {
	InflectionDate string         `json:"inflection_date"`        // "2023-06-01"
	BracketWindow  string         `json:"bracket_window"`         // "12m"
	BracketStart   string         `json:"bracket_start"`          // "2022-06-01"
	BracketEnd     string         `json:"bracket_end"`            // "2026-06-15"
	Strategy       string         `json:"strategy"`               // "search_default_relevance" | "random"
	Buckets        []SampleBucket `json:"buckets"`
}

// SampleBucket records the extraction result for one month (or week) bucket
// in the pre-bracket sparse slice.
type SampleBucket struct {
	Month     string `json:"month"`               // "2022-01" or "2022-01-W1" for weekly sub-buckets
	Target    int    `json:"target"`              // requested N per bucket
	Actual    int    `json:"actual"`              // PRs emitted
	Total     int    `json:"total"`               // totalCount from GraphQL search
	Truncated bool   `json:"truncated,omitempty"` // true when Total > 1000 (search cap)
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
	if len(other.ConfigDepth) > 0 && p.ConfigDepth == nil {
		p.ConfigDepth = make(map[string]string)
	}
	for k, v := range other.ConfigDepth {
		if _, ok := p.ConfigDepth[k]; !ok {
			p.ConfigDepth[k] = v
		}
	}
	if other.Sampling != nil && p.Sampling == nil {
		p.Sampling = other.Sampling
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
