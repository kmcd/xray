package sentry

import (
	"log/slog"
	"net/http"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/ratelimit"
)

// DefaultBaseURL is the Sentry v0 API base URL.
const DefaultBaseURL = "https://sentry.io/api/0"

// Connector implements connector.Connector against the Sentry v0 API.
type Connector struct {
	httpClient *http.Client
	log        *slog.Logger
	token      string
	org        string
	projects   map[string]string // sentry project slug -> repo slug
	baseURL    string
	rl         *ratelimit.Transport
}

// Config is the connector's input. BaseURL is exposed only for tests.
type Config struct {
	Token        string
	Organization string
	Projects     map[string]string
	BaseURL      string
}

// New builds a Connector wired to the rate-limit transport. The token is
// the only authentication Sentry v0 exposes for these endpoints; an empty
// token is accepted at construction time and surfaced as a 401 at Ping.
func New(cfg config.SentryConn, log *slog.Logger) (*Connector, error) {
	if log == nil {
		log = slog.Default()
	}
	rl := &ratelimit.Transport{
		Base:   http.DefaultTransport,
		Policy: ratelimit.DefaultPolicy(),
		Log:    log,
	}
	client := &http.Client{Transport: rl}
	return &Connector{
		httpClient: client,
		log:        log,
		token:      cfg.Token,
		org:        cfg.Organization,
		projects:   cfg.Projects,
		baseURL:    DefaultBaseURL,
		rl:         rl,
	}, nil
}

// BudgetSnapshot returns the current rate-limit budget for this connector.
func (c *Connector) BudgetSnapshot() map[string]ratelimit.BudgetState {
	if c.rl == nil {
		return nil
	}
	return c.rl.Snapshot()
}

// Name returns the stable connector name used in `source` columns and the
// manifest's `extraction_provenance` entries.
func (c *Connector) Name() string { return "sentry" }

// authHeader attaches the bearer token and JSON Accept header to a request.
// All Sentry v0 requests use this header.
func (c *Connector) authHeader(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
}
