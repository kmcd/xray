package main

import (
	"log/slog"

	"github.com/kmcd/xray/internal/config"
	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/connectors/bugsnag"
	"github.com/kmcd/xray/internal/connectors/circleci"
	xgithub "github.com/kmcd/xray/internal/connectors/github"
	"github.com/kmcd/xray/internal/connectors/githubactions"
	"github.com/kmcd/xray/internal/connectors/honeycomb"
	"github.com/kmcd/xray/internal/connectors/sentry"
)

// buildConnectors instantiates every connector configured in cfg.
// Connector ordering follows the declaration order in CLAUDE.md so that
// honeycomb's first-repo selection (the v1 fallback for its repo-agnostic
// model) is deterministic.
func buildConnectors(cfg *config.Config, log *slog.Logger) ([]connector.Connector, error) {
	var out []connector.Connector

	if cfg.Connectors.GitHub != nil {
		c, err := xgithub.New(*cfg.Connectors.GitHub, log)
		if err != nil {
			return nil, err
		}
		c.SetCaptureHarnessContent(cfg.CaptureHarnessContent)
		out = append(out, c)
	}
	if cfg.Connectors.GitHubActions != nil {
		c, err := githubactions.New(*cfg.Connectors.GitHubActions, log)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if cfg.Connectors.CircleCI != nil {
		c, err := circleci.New(*cfg.Connectors.CircleCI, log)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if cfg.Connectors.Sentry != nil {
		c, err := sentry.New(*cfg.Connectors.Sentry, log)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if cfg.Connectors.Bugsnag != nil {
		c, err := bugsnag.New(*cfg.Connectors.Bugsnag, log)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if cfg.Connectors.Honeycomb != nil {
		c, err := honeycomb.New(*cfg.Connectors.Honeycomb, log)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
