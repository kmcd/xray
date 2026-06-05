package githubactions

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/ratelimit"
)

// Connector implements the github_actions connector.
type Connector struct {
	cfg    config.GitHubActionsConn
	log    *slog.Logger
	client *github.Client
}

// Config is an alias exported for symmetry with other connectors.
type Config = config.GitHubActionsConn

// New constructs a Connector. The supplied token is treated as authoritative;
// inheritance from [connectors.github] is applied during config Load.
func New(cfg config.GitHubActionsConn, log *slog.Logger) (*Connector, error) {
	if cfg.Token == "" {
		return nil, errors.New("githubactions: missing token (and no inherited github token)")
	}
	if log == nil {
		log = slog.Default()
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})
	base := oauth2.NewClient(context.Background(), ts)
	// Wrap the underlying transport with the shared rate-limit/retry helper.
	var underlying = http.DefaultTransport
	if t, ok := base.Transport.(*oauth2.Transport); ok {
		if t.Base != nil {
			underlying = t.Base
		}
		t.Base = &ratelimit.Transport{
			Base:   underlying,
			Policy: ratelimit.DefaultPolicy(),
			Log:    log,
		}
	} else {
		base.Transport = &ratelimit.Transport{
			Base:   base.Transport,
			Policy: ratelimit.DefaultPolicy(),
			Log:    log,
		}
	}

	gh := github.NewClient(base)

	return &Connector{
		cfg:    cfg,
		log:    log,
		client: gh,
	}, nil
}

// Name implements connector.Connector.
func (c *Connector) Name() string { return "github_actions" }
