package github

import (
	"context"
	"log/slog"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/gitcli"
	"github.com/kmcd/xray/internal/model"
)

// extractCommits drives git log over the local clone and emits commits,
// commit_files, and commit_coauthors rows. Signature verification is filled
// in via a single batched GraphQL request per ~100 commits (see enrich.go
// and issue #64). landed_via_pr is filled in postprocess from a join
// against pr_commits (issue #75) and is left nil here.
//
// mm canonicalises (name, email) identities through the repo's .mailmap
// before the hashHandle helper emits the opaque "h_<digits>" token; bot
// classification still consults the pre-hash name so the signal isn't lost.
func (c *Connector) extractCommits(ctx context.Context, repo connector.Repo, window connector.Window, sink connector.Sink, prov *connector.Provenance, mm *gitcli.Mailmap) {
	if repo.Clone == "" {
		// No clone -> no commits. Caller already recorded the clone failure.
		return
	}
	records, err := c.git.LogNumstat(ctx, repo.Clone, window.Start, window.End, repo.DefaultBranch)
	if err != nil {
		prov.Errors["commits"] = err.Error()
		c.log.Warn("github: git log failed",
			slog.String("repo", repo.Slug),
			slog.String("error", err.Error()),
		)
		return
	}

	owner, name, slugOK := splitSlug(repo.Slug)

	// Batch-enrich every SHA up front so the per-record loop is a pure
	// in-memory join. Replaces O(commits * 2) REST round-trips with
	// O(commits / 100) GraphQL POSTs.
	var enrichment map[string]commitEnrichment
	if slugOK && len(records) > 0 {
		shas := make([]string, 0, len(records))
		for _, rec := range records {
			shas = append(shas, rec.SHA)
		}
		var enrichErr error
		enrichment, enrichErr = c.enrichCommits(ctx, owner, name, shas)
		if enrichErr != nil {
			// enrichCommits already logs per-batch failures; this is only
			// hit if the entire pass aborted (e.g. context cancelled).
			// Don't fail the connector — the columns stay nil, which the
			// analyser treats as unknown.
			c.log.Warn("github: batched commit enrichment aborted",
				slog.String("repo", repo.Slug),
				slog.String("error", enrichErr.Error()),
			)
		}
	}

	prog := newProgress(c.log, repo.Slug, "commits")
	defer prog.done()
	var cxPairs []complexityPair

	// Hot-table batches: commits, commit_files, commit_coauthors. Each flushes
	// every batchChunk Adds inside one explicit tx, then increments
	// prov.RowsReturned by the committed count at Commit time. emitDefects
	// (called inside this loop) hits a Cold table via per-row Insert — that
	// runs between Adds when the batch holds no open tx, so no deadlock on
	// the single-writer SQLite connection.
	cb := openCommitsBatch(sink)
	defer cb.Rollback()
	cfb := openCommitFilesBatch(sink)
	defer cfb.Rollback()
	ccb := openCommitCoauthorsBatch(sink)
	defer ccb.Rollback()

	for _, rec := range records {
		if ctx.Err() != nil {
			prov.PaginationComplete = false
			break
		}
		prog.tick()

		authorName, authorEmail := mm.Resolve(rec.AuthorHandle, rec.AuthorEmail)
		committerName, committerEmail := mm.Resolve(rec.CommitterHandle, rec.CommitterEmail)
		row := model.Commit{
			SHA:             rec.SHA,
			Repo:            repo.Slug,
			AuthorHandle:    hashHandle(canonicalCommitIdent(authorName, authorEmail)),
			CommitterHandle: hashHandle(canonicalCommitIdent(committerName, committerEmail)),
			AuthoredAt:      rec.AuthoredAt,
			CommittedAt:     rec.CommittedAt,
			MessageSubject:  rec.Subject,
			AuthorIsBot:     isBot(rec.AuthorHandle),
			CommitterIsBot:  isBot(rec.CommitterHandle),
			IsRevert:        parseIsRevert(rec.Subject, rec.Body),
			RevertsSHA:      parseRevertsSHA(rec.Body),
			HasHotfixMarker: parseHasHotfixMarker(rec.Body),
			IsMerge:         len(rec.ParentSHAs) > 1,
		}

		for _, f := range rec.Files {
			row.Additions += f.Additions
			row.Deletions += f.Deletions
			row.FilesChanged++
		}

		if en, ok := enrichment[rec.SHA]; ok {
			row.SignatureVerified = en.SignatureVerified
		}
		// row.LandedViaPR stays nil here; postprocess fills it via a
		// join against pr_commits (issue #75).

		if err := cb.Add(row); err != nil {
			if prov.Errors["commits"] == "" {
				prov.Errors["commits"] = err.Error()
			}
			c.log.Warn("github: insert commit", slog.String("sha", rec.SHA), slog.String("error", err.Error()))
		}

		// Defect emission: parse ticket references out of the commit
		// subject and body, then discard the body text (per the no-raw-
		// bodies rule). Commit-only references use committed_at as
		// opened_at and leave closed_at null.
		emitDefects(sink, repo.Slug, "commit_message", rec.SHA,
			rec.Subject+"\n"+rec.Body, rec.CommittedAt, nil, prov)

		// Per-file rows. Collect non-deleted, non-excluded paths for the
		// complexity history batch at the end of the loop.
		for _, f := range rec.Files {
			cf := model.CommitFile{
				CommitSHA:  rec.SHA,
				Repo:       repo.Slug,
				Path:       f.Path,
				Additions:  f.Additions,
				Deletions:  f.Deletions,
				ChangeType: f.ChangeType,
				PrevPath:   f.PrevPath,
			}
			if err := cfb.Add(cf); err != nil {
				if prov.Errors["commit_files"] == "" {
					prov.Errors["commit_files"] = err.Error()
				}
			}
			if f.ChangeType != "D" && !complexityHistoryExcluded(f.Path) {
				cxPairs = append(cxPairs, complexityPair{rec.SHA, f.Path})
			}
		}

		// Coauthor rows. Track trailer handles so committerDistinctCoauthor
		// doesn't emit a duplicate PK when the committer is also listed as a
		// Co-authored-by trailer (same handle, different source → OR REPLACE
		// would silently drop one and inflate the manifest count).
		trailerHandles := map[string]bool{}
		for _, ca := range trailerCoauthors(rec, repo.Slug, mm) {
			trailerHandles[ca.Handle] = true
			if err := ccb.Add(ca); err != nil {
				if prov.Errors["commit_coauthors"] == "" {
					prov.Errors["commit_coauthors"] = err.Error()
				}
			}
		}
		if ca, ok := committerDistinctCoauthor(rec, repo.Slug, mm); ok && !trailerHandles[ca.Handle] {
			if err := ccb.Add(ca); err != nil {
				if prov.Errors["commit_coauthors"] == "" {
					prov.Errors["commit_coauthors"] = err.Error()
				}
			}
		}
	}

	// Commit the three hot-table batches before the complexity history
	// pass runs. Each Commit() flushes the buffered tail and returns the
	// total rows that landed; prov.RowsReturned increments by exactly that
	// count, satisfying the "increment only after successful tx.Commit()"
	// invariant. A flush error sets the per-table errors entry first-wins.
	commitBatch(cb, prov, "commits")
	commitBatch(cfb, prov, "commit_files")
	commitBatch(ccb, prov, "commit_coauthors")

	// Batch-fetch file content via one git cat-file --batch subprocess and
	// emit file_complexity_history rows. Replaces O(N) git-show subprocesses.
	c.extractComplexityHistoryBatch(ctx, repo, cxPairs, sink, prov)
}
