package bugsnag

import (
	"log/slog"
	"net/http"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/ratelimit"
)

// DefaultBaseURL is the Bugsnag Data Access API base URL.
const DefaultBaseURL = "https://api.bugsnag.com"

// APIVersion is the value sent in the X-Version header per Bugsnag's API
// versioning scheme.
const APIVersion = "2"

// DefaultMaxWindowDays is the cap applied to the extraction window when
// MaxWindowDays is not set in config. 60 matches the Select/Preferred plan
// retention horizon — the most common paid Bugsnag tier.
const DefaultMaxWindowDays = 60

// Connector implements connector.Connector against the Bugsnag Data Access
// API. It populates `incidents`.
type Connector struct {
	httpClient *http.Client
	log        *slog.Logger
	token      string
	baseURL    string
	// projects maps bugsnag project ID -> repo slug. The TOML field uses the
	// label "projects" (per the spec sample) but the Bugsnag API addresses
	// projects by ID; the value the operator pastes in the map key is the
	// project ID from Bugsnag's URL or API.
	projects      map[string]string
	maxWindowDays int
	rl            *ratelimit.Transport
}

// Config is the connector's input. BaseURL is exposed only for tests.
type Config struct {
	Token    string
	BaseURL  string
	Projects map[string]string
}

// New builds a Connector wired to the rate-limit transport.
func New(cfg config.BugsnagConn, log *slog.Logger) (*Connector, error) {
	if log == nil {
		log = slog.Default()
	}
	rl := &ratelimit.Transport{
		Base:   ratelimit.NewHTTPTransport(),
		Policy: ratelimit.DefaultPolicy(),
		Log:    log,
	}
	client := &http.Client{Transport: rl}
	mwd := cfg.MaxWindowDays
	if mwd <= 0 {
		mwd = DefaultMaxWindowDays
	}
	return &Connector{
		httpClient:    client,
		log:           log,
		token:         cfg.Token,
		baseURL:       DefaultBaseURL,
		projects:      cfg.Projects,
		maxWindowDays: mwd,
		rl:            rl,
	}, nil
}

// Name returns the stable connector name used in `source` columns and the
// manifest's `extraction_provenance` entries.
func (c *Connector) Name() string { return "bugsnag" }

// BudgetSnapshot returns the current rate-limit budget for this connector.
func (c *Connector) BudgetSnapshot() map[string]ratelimit.BudgetState {
	if c.rl == nil {
		return nil
	}
	return c.rl.Snapshot()
}

// authHeader attaches Bugsnag's auth headers to a request. All Data Access
// API requests use these headers, including /user used for Ping.
func (c *Connector) authHeader(req *http.Request) {
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("X-Version", APIVersion)
	req.Header.Set("Accept", "application/json")
}
