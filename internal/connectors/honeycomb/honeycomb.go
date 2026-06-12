package honeycomb

import (
	"log/slog"
	"net/http"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/ratelimit"
)

// DefaultBaseURL is the Honeycomb v1 API base URL.
const DefaultBaseURL = "https://api.honeycomb.io/1"

// Connector implements connector.Connector against the Honeycomb v1 API.
//
// Honeycomb has no per-repo concept. Markers are attributed to repos via
// the GitHub commit URL embedded in each marker; markers with no URL or a
// URL that doesn't match the current repo are skipped.
type Connector struct {
	httpClient *http.Client
	log        *slog.Logger
	token      string
	dataset    string
	baseURL    string
	rl         *ratelimit.Transport
	noCache    bool
}

// Config is the connector's input. BaseURL is exposed only for tests.
type Config struct {
	Token   string
	Dataset string
	BaseURL string
}

// New builds a Connector wired to the rate-limit transport. noCache disables
// the on-disk marker cache for this connector instance.
func New(cfg config.HoneycombConn, log *slog.Logger, noCache bool) (*Connector, error) {
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
		dataset:    cfg.Dataset,
		baseURL:    DefaultBaseURL,
		rl:         rl,
		noCache:    noCache,
	}, nil
}

// Name returns the stable connector name used in `source` columns and the
// manifest's `extraction_provenance` entries.
func (c *Connector) Name() string { return "honeycomb" }

// BudgetSnapshot returns the current rate-limit budget for this connector.
func (c *Connector) BudgetSnapshot() map[string]ratelimit.BudgetState {
	if c.rl == nil {
		return nil
	}
	return c.rl.Snapshot()
}

// authHeader attaches the X-Honeycomb-Team header to a request.
func (c *Connector) authHeader(req *http.Request) {
	req.Header.Set("X-Honeycomb-Team", c.token)
	req.Header.Set("Accept", "application/json")
}

