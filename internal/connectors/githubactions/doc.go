// Package githubactions implements the GitHub Actions connector. It populates
// the builds and build_jobs tables from the Workflow Runs / Jobs APIs and the
// deploys table (source=github_actions) from the Deployments API.
//
// The connector shares the github connector's token by default; configuration
// is applied during config.Load so this package treats cfg.Token as
// authoritative. Same API host as github, so no separate credential is needed.
package githubactions
