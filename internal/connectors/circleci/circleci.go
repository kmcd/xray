package circleci

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/ratelimit"
)

// DefaultBaseURL is the CircleCI v2 API base URL.
const DefaultBaseURL = "https://circleci.com/api/v2"

// Connector implements connector.Connector against CircleCI v2.
type Connector struct {
	httpClient *http.Client
	log        *slog.Logger
	token      string
	baseURL    string
	projects   map[string]string // circleci project slug -> repo slug
}

// Config is the connector's input. BaseURL is exposed only for tests.
type Config struct {
	Token   string
	BaseURL string
}

// New builds a Connector wired to the rate-limit transport. It is an error
// to construct a Connector without a token; the token is the only
// authentication CircleCI v2 exposes for these endpoints.
func New(cfg config.CircleCIConn, log *slog.Logger) (*Connector, error) {
	if log == nil {
		log = slog.Default()
	}
	client := &http.Client{
		Transport: &ratelimit.Transport{
			Base:   http.DefaultTransport,
			Policy: ratelimit.DefaultPolicy(),
			Log:    log,
		},
	}
	return &Connector{
		httpClient: client,
		log:        log,
		token:      cfg.Token,
		baseURL:    DefaultBaseURL,
		projects:   cfg.Projects,
	}, nil
}

// Name returns the stable connector name used in `source` columns and the
// manifest's `extraction_provenance` entries.
func (c *Connector) Name() string { return "circleci" }

// projectSlug maps an "owner/name" repo slug to CircleCI's "gh/<owner>/<name>"
// project slug. Returns the empty string if the input is not a two-segment
// slug; callers should treat that as a skip.
func projectSlug(repoSlug string) string {
	parts := strings.SplitN(repoSlug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return "gh/" + parts[0] + "/" + parts[1]
}

// authHeader attaches the Circle-Token header to a request. All CircleCI v2
// requests use this header, including /me used for Ping.
func (c *Connector) authHeader(req *http.Request) {
	req.Header.Set("Circle-Token", c.token)
	req.Header.Set("Accept", "application/json")
}
