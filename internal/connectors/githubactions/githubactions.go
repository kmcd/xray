package githubactions

import (
	"context"
	"errors"
	"log/slog"

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
	rl     *ratelimit.Transport
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
	rl := &ratelimit.Transport{Policy: ratelimit.DefaultPolicy(), Log: log}
	if t, ok := base.Transport.(*oauth2.Transport); ok {
		rl.Base = ratelimit.NewHTTPTransport()
		t.Base = rl
	} else {
		rl.Base = base.Transport
		base.Transport = rl
	}

	gh := github.NewClient(base)

	return &Connector{
		cfg:    cfg,
		log:    log,
		client: gh,
		rl:     rl,
	}, nil
}

// Name implements connector.Connector.
func (c *Connector) Name() string { return "github_actions" }

// BudgetSnapshot returns the current rate-limit budget for this connector.
func (c *Connector) BudgetSnapshot() map[string]ratelimit.BudgetState {
	if c.rl == nil {
		return nil
	}
	return c.rl.Snapshot()
}
