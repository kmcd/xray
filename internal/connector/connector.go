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
