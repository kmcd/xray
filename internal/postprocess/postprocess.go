package postprocess

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// Stats is the summary returned by Run.
type Stats struct {
	IncidentsLinked    int
	DeploysRolledBack  int
	LandedViaPRMatched int
}

// Run executes cross-cutting linkage passes against the populated SQLite
// store. It is called by internal/run after every connector has completed
// and before the manifest is written.
func Run(ctx context.Context, db *sql.DB, log *slog.Logger) (Stats, error) {
	var stats Stats
	if db == nil {
		return stats, fmt.Errorf("postprocess: nil db")
	}
	if log == nil {
		log = slog.Default()
	}

	linked, err := linkIncidents(ctx, db, log)
	if err != nil {
		return stats, fmt.Errorf("postprocess: incidents: %w", err)
	}
	stats.IncidentsLinked = linked

	rolled, err := linkDeployRollbacks(ctx, db, log)
	if err != nil {
		return stats, fmt.Errorf("postprocess: deploys: %w", err)
	}
	stats.DeploysRolledBack = rolled

	matched, err := deriveLandedViaPR(ctx, db, log)
	if err != nil {
		return stats, fmt.Errorf("postprocess: landed_via_pr: %w", err)
	}
	stats.LandedViaPRMatched = matched

	return stats, nil
}

// deriveLandedViaPR fills in commits.landed_via_pr from a join against
// pr_commits. Replaces the per-commit associatedPullRequests subquery that
// used to ride along with the signature-verified enrichment (issue #75).
//
// Semantic shift from the pre-#75 GraphQL path: that query asked GitHub
// "has any PR ever included this commit?" — global, lifetime-of-the-repo.
// This pass asks "does a PR inside the current extraction window include
// this commit?" — window-restricted. An in-window commit whose PR closed
// before window.start (or was missed for any reason) reports false here
// where the old code reported true.
//
// Commits unmatched by pr_commits get landed_via_pr = 0 (false), not NULL.
// pr_commits is a complete listing within the window, so absence is a
// real "didn't land via PR" signal — analyser should see false, not
// unknown.
func deriveLandedViaPR(ctx context.Context, db *sql.DB, log *slog.Logger) (int, error) {
	// Mark true for any commit whose (repo, sha) appears in pr_commits.
	res, err := db.ExecContext(ctx, `
		UPDATE commits
		SET landed_via_pr = 1
		WHERE EXISTS (
			SELECT 1 FROM pr_commits
			WHERE pr_commits.repo = commits.repo
			  AND pr_commits.sha = commits.sha
		)
	`)
	if err != nil {
		return 0, fmt.Errorf("update landed_via_pr=true: %w", err)
	}
	matched, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected (true): %w", err)
	}

	// Fill the rest with false. landed_via_pr is the only column populated
	// in postprocess on commits, so any remaining NULL came through the
	// extractor's nil enrichment slot and is safe to clear to 0.
	if _, err := db.ExecContext(ctx, `
		UPDATE commits
		SET landed_via_pr = 0
		WHERE landed_via_pr IS NULL
	`); err != nil {
		return int(matched), fmt.Errorf("update landed_via_pr=false: %w", err)
	}

	log.Debug("postprocess: landed_via_pr derived",
		slog.Int64("matched_true", matched),
	)
	return int(matched), nil
}

// linkIncidents resolves incidents.deploy_id and incidents.commit_sha from
// release_ref. For each row where release_ref is non-empty and deploy_id
// is empty:
//  1. Match the most recent deploy in the same repo where release_tag =
//     release_ref OR version = release_ref. If found, set deploy_id and
//     commit_sha.
//  2. Otherwise, match a release in the same repo where tag = release_ref.
//     If found, set commit_sha only.
func linkIncidents(ctx context.Context, db *sql.DB, log *slog.Logger) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, repo, source, release_ref
		FROM incidents
		WHERE release_ref IS NOT NULL
		  AND release_ref != ''
		  AND (deploy_id IS NULL OR deploy_id = '')
	`)
	if err != nil {
		return 0, fmt.Errorf("query incidents: %w", err)
	}
	type pending struct {
		id, repo, source, ref string
	}
	var todo []pending
	for rows.Next() {
		if ctx.Err() != nil {
			_ = rows.Close()
			return 0, ctx.Err()
		}
		var p pending
		if err := rows.Scan(&p.id, &p.repo, &p.source, &p.ref); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan incident: %w", err)
		}
		todo = append(todo, p)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	linked := 0
	for _, p := range todo {
		if ctx.Err() != nil {
			return linked, ctx.Err()
		}

		// 1. Try a matching deploy. Pick the most recent by deployed_at.
		var deployID, commitSHA sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT id, COALESCE(commit_sha, '')
			FROM deploys
			WHERE repo = ?
			  AND (release_tag = ? OR version = ?)
			ORDER BY deployed_at DESC
			LIMIT 1
		`, p.repo, p.ref, p.ref).Scan(&deployID, &commitSHA)
		switch err {
		case nil:
			// Always set deploy_id; only overwrite commit_sha when the
			// connector did not already populate it (e.g. from Bugsnag
			// release.revision), so the per-error SHA is preserved over
			// the coarser deploy-level SHA.
			if _, err := db.ExecContext(ctx, `
				UPDATE incidents
				SET deploy_id = ?,
				    commit_sha = CASE WHEN (commit_sha IS NULL OR commit_sha = '') THEN ? ELSE commit_sha END
				WHERE repo = ? AND source = ? AND id = ?
			`, deployID.String, commitSHA.String, p.repo, p.source, p.id); err != nil {
				return linked, fmt.Errorf("update incident %s: %w", p.id, err)
			}
			linked++
			continue
		case sql.ErrNoRows:
			// fall through to release lookup
		default:
			return linked, fmt.Errorf("lookup deploy for incident %s: %w", p.id, err)
		}

		// 2. Try a matching release for commit_sha alone.
		var sha sql.NullString
		err = db.QueryRowContext(ctx, `
			SELECT COALESCE(sha, '')
			FROM releases
			WHERE repo = ? AND tag = ?
		`, p.repo, p.ref).Scan(&sha)
		switch err {
		case nil:
			if sha.String == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, `
				UPDATE incidents
				SET commit_sha = ?
				WHERE repo = ? AND source = ? AND id = ?
				  AND (commit_sha IS NULL OR commit_sha = '')
			`, sha.String, p.repo, p.source, p.id); err != nil {
				return linked, fmt.Errorf("update incident %s commit_sha: %w", p.id, err)
			}
			linked++
		case sql.ErrNoRows:
			// nothing matched; leave alone
		default:
			return linked, fmt.Errorf("lookup release for incident %s: %w", p.id, err)
		}
	}

	log.Debug("postprocess: incidents linked",
		slog.Int("count", linked),
		slog.Int("considered", len(todo)),
	)
	return linked, nil
}

// linkDeployRollbacks walks each (repo, environment) deploy timeline in
// ascending deployed_at order. The heuristic: a deploy D[i] is classified
// as a rollback of D[i-1] when D[i].commit_sha == D[i-2].commit_sha
// AND D[i].commit_sha != D[i-1].commit_sha AND the commit_sha is non-empty
// AND D[i-1].status != "success" (per ADR 017 — the predecessor must be a
// failed/errored/already-rolled-back deploy for this to be a real rollback
// rather than a routine re-deploy of a green commit).
// D[i] is updated with supersedes_deploy_id = D[i-1].id; D[i-1] is marked
// rolled_back.
func linkDeployRollbacks(ctx context.Context, db *sql.DB, log *slog.Logger) (int, error) {
	// Pull every deploy with a non-empty environment in a single scan,
	// ordered so we can walk groups in place.
	rows, err := db.QueryContext(ctx, `
		SELECT id, repo, source, environment, COALESCE(commit_sha, ''), deployed_at, COALESCE(status, '')
		FROM deploys
		WHERE environment IS NOT NULL AND environment != ''
		ORDER BY repo ASC, environment ASC, deployed_at ASC, id ASC
	`)
	if err != nil {
		return 0, fmt.Errorf("query deploys: %w", err)
	}

	type deploy struct {
		id, repo, source, env, sha, deployedAt, status string
	}
	var all []deploy
	for rows.Next() {
		if ctx.Err() != nil {
			_ = rows.Close()
			return 0, ctx.Err()
		}
		var d deploy
		if err := rows.Scan(&d.id, &d.repo, &d.source, &d.env, &d.sha, &d.deployedAt, &d.status); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan deploy: %w", err)
		}
		all = append(all, d)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	rolled := 0
	// Walk groups; identify rollbacks via the three-deploy heuristic
	// documented above.
	i := 0
	for i < len(all) {
		j := i
		for j < len(all) && all[j].repo == all[i].repo && all[j].env == all[i].env {
			j++
		}
		group := all[i:j]
		for k := 2; k < len(group); k++ {
			d := group[k]
			p1 := group[k-1]
			p2 := group[k-2]
			if d.sha == "" {
				continue
			}
			// ADR 017: gate the heuristic on the predecessor being a
			// non-success deploy. A re-deploy of a green commit (canary
			// advance, autoscaling, blue/green flip back) is not a
			// rollback even when the commit pattern matches.
			if p1.status == "success" {
				continue
			}
			if d.sha == p2.sha && d.sha != p1.sha {
				if _, err := db.ExecContext(ctx, `
					UPDATE deploys SET supersedes_deploy_id = ?
					WHERE repo = ? AND source = ? AND id = ?
				`, p1.id, d.repo, d.source, d.id); err != nil {
					return rolled, fmt.Errorf("update deploy %s supersedes: %w", d.id, err)
				}
				if _, err := db.ExecContext(ctx, `
					UPDATE deploys SET rolled_back = 1
					WHERE repo = ? AND source = ? AND id = ?
				`, p1.repo, p1.source, p1.id); err != nil {
					return rolled, fmt.Errorf("update deploy %s rolled_back: %w", p1.id, err)
				}
				rolled++
			}
		}
		i = j
	}

	log.Debug("postprocess: deploys rolled back",
		slog.Int("count", rolled),
		slog.Int("considered", len(all)),
	)
	return rolled, nil
}
