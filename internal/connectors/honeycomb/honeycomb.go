package honeycomb

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/ratelimit"
)

// DefaultBaseURL is the Honeycomb v1 API base URL.
const DefaultBaseURL = "https://api.honeycomb.io/1"

// Connector implements connector.Connector against the Honeycomb v1 API.
//
// Honeycomb has no per-repo concept. To attribute deploys to a repo, the
// connector lazily picks the first repo (alphabetical by slug) it sees
// across Extract calls and emits all data tagged to that repo. Subsequent
// Extract calls for other repos return an empty Provenance.
type Connector struct {
	httpClient *http.Client
	log        *slog.Logger
	token      string
	dataset    string
	baseURL    string
	rl         *ratelimit.Transport

	mu        sync.Mutex
	firstRepo string
}

// Config is the connector's input. BaseURL is exposed only for tests.
type Config struct {
	Token   string
	Dataset string
	BaseURL string
}

// New builds a Connector wired to the rate-limit transport.
func New(cfg config.HoneycombConn, log *slog.Logger) (*Connector, error) {
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

// chooseRepo lazily fixes the chosen repo slug on the first Extract call
// and returns true only for that repo. Subsequent Extract calls for other
// repos return false and become no-ops to avoid duplicate emission.
//
// Honeycomb exposes no per-repo concept, so emitting the same dataset's
// markers under every repo would inflate counts. The run scheduler iterates
// repos in a stable order; whichever repo arrives first owns the data.
func (c *Connector) chooseRepo(slug string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.firstRepo == "" {
		c.firstRepo = slug
	}
	return c.firstRepo == slug
}

// chosenRepo returns the repo slug previously fixed by chooseRepo. Empty
// before the first Extract call.
func (c *Connector) chosenRepo() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstRepo
}
