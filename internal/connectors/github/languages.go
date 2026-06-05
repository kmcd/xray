package github

import (
	"context"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// extractLanguages calls ListLanguages and emits a repo_languages row per
// language.
func (c *Connector) extractLanguages(ctx context.Context, repo connector.Repo, sink connector.Sink, prov *connector.Provenance) error {
	owner, name, ok := splitSlug(repo.Slug)
	if !ok {
		return nil
	}
	langs, _, err := c.rest.Repositories.ListLanguages(ctx, owner, name)
	if err != nil {
		return err
	}
	for lang, bytes := range langs {
		row := model.RepoLanguage{Repo: repo.Slug, Language: lang, Bytes: int64(bytes)}
		if err := sink.InsertRepoLanguage(row); err != nil {
			if prov.Errors["repo_languages"] == "" {
				prov.Errors["repo_languages"] = err.Error()
			}
			continue
		}
		prov.RowsReturned["repo_languages"]++
	}
	return nil
}
