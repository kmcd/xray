package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	gh "github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/gitcli"
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

	// per-connector caches that are safe to reuse across repos.
	mu            sync.Mutex
	templateCache map[string]*template // repo slug -> parsed template (nil if absent)
}

// SetCaptureHarnessContent toggles the harness-artifact content-capture
// flag. The constructor accepts only the GitHub connector config; the
// run wiring sets this from the top-level config.CaptureHarnessContent
// before the connector is invoked.
func (c *Connector) SetCaptureHarnessContent(v bool) {
	c.capture = v
}

// Name returns the connector name as recorded in extraction provenance.
func (c *Connector) Name() string { return "github" }

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
	if tr, ok := httpClient.Transport.(*oauth2.Transport); ok {
		tr.Base = &ratelimit.Transport{
			Base:   tr.Base,
			Policy: ratelimit.DefaultPolicy(),
			Log:    log,
		}
	} else {
		httpClient.Transport = &ratelimit.Transport{
			Base:   httpClient.Transport,
			Policy: ratelimit.DefaultPolicy(),
			Log:    log,
		}
	}

	rest := gh.NewClient(httpClient)
	gql := githubv4.NewClient(httpClient)

	return &Connector{
		cfg:           cfg,
		log:           log,
		httpClient:    httpClient,
		rest:          rest,
		gql:           gql,
		git:           &gitcli.Client{Log: log},
		templateCache: map[string]*template{},
	}, nil
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
	c.gql = githubv4.NewEnterpriseClient(strings.TrimSuffix(rawURL, "/")+"/graphql", c.httpClient)
	return nil
}
