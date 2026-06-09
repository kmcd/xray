package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// enrichBatchSize is the maximum number of commits batched into a single
// GraphQL query. The earlier 25-alias ceiling was forced by the
// associatedPullRequests subquery (server-side timeouts at 100). With the
// query trimmed to signature-only (issue #75), the per-alias cost dropped
// far enough to restore 100. landed_via_pr is now derived in postprocess
// via a join against pr_commits, so no PR-association traversal happens
// during enrichment.
const enrichBatchSize = 100

// enrichBatchDelay is a small pause between consecutive batched GraphQL
// POSTs. GitHub's GraphQL API has a *secondary* rate limit that triggers
// on bursts and returns 403; the ratelimit transport recognises and
// retries that case, but the delay still helps avoid tripping it in the
// first place. Shrunk from 500ms to 250ms in #75 — the lighter query
// (signature only) burns fewer points per request, so the inter-batch
// gap can be tighter without re-tripping the secondary limit.
const enrichBatchDelay = 250 * time.Millisecond

// commitEnrichment is the per-SHA data the GraphQL batched query collects.
// SignatureVerified is a pointer so callers can distinguish "not populated"
// (analyser reads as unknown) from a fetched false.
//
// landed_via_pr used to live here too, derived from associatedPullRequests.
// As of #75 it is filled in postprocess via a join against pr_commits and
// no longer carried on this struct.
type commitEnrichment struct {
	SignatureVerified *bool
}

// enrichCommits asks GitHub's GraphQL API for signature_verified for every
// supplied commit SHA, in batches of enrichBatchSize aliases per request.
// Returns a map keyed by SHA; absent keys indicate the API did not return
// data for that commit (the caller treats the column as unknown).
//
// Replaces the per-commit REST round-trips that previously made the github
// connector's commit phase O(commits * 2) round-trips. See issue #64. The
// associatedPullRequests subquery moved out in #75 — landed_via_pr is
// derived from pr_commits in postprocess.
func (c *Connector) enrichCommits(ctx context.Context, owner, name string, shas []string) (map[string]commitEnrichment, error) {
	out := make(map[string]commitEnrichment, len(shas))
	for i := 0; i < len(shas); i += enrichBatchSize {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if i > 0 {
			// Space batches to dodge GitHub's secondary (anti-burst) rate
			// limit on the GraphQL endpoint.
			select {
			case <-time.After(enrichBatchDelay):
			case <-ctx.Done():
				return out, ctx.Err()
			}
		}
		end := i + enrichBatchSize
		if end > len(shas) {
			end = len(shas)
		}
		if err := c.enrichOneBatch(ctx, owner, name, shas[i:end], out); err != nil {
			c.log.Warn("github: graphql enrich batch failed",
				slog.Int("batch_start", i),
				slog.Int("batch_size", end-i),
				slog.String("error", err.Error()),
			)
			// Continue to the next batch — analyser treats absent rows as
			// unknown, which is preferable to aborting the whole connector
			// over a transient enrichment failure.
		}
	}
	return out, nil
}

// enrichOneBatch issues one GraphQL POST for the supplied slice of SHAs and
// merges the parsed enrichment into out.
func (c *Connector) enrichOneBatch(ctx context.Context, owner, name string, shas []string, out map[string]commitEnrichment) error {
	if len(shas) == 0 {
		return nil
	}

	// Construct the dynamic query. Each commit gets its own alias on
	// repository.object(oid:...). Owner, repo, and SHAs are all constrained
	// to safe character sets so inline interpolation is acceptable; we
	// still defensively strip any quote characters that could close the
	// literal early.
	var sb strings.Builder
	sb.WriteString(`query { repository(owner: "`)
	sb.WriteString(graphqlSafe(owner))
	sb.WriteString(`", name: "`)
	sb.WriteString(graphqlSafe(name))
	sb.WriteString(`") {`)
	emitted := 0
	for i, sha := range shas {
		if !isFullSHA(sha) {
			continue
		}
		fmt.Fprintf(&sb, ` a%d: object(oid: "%s") { ... on Commit { signature { isValid } } }`, i, sha)
		emitted++
	}
	sb.WriteString(` } }`)
	if emitted == 0 {
		return nil
	}

	body, err := json.Marshal(map[string]string{"query": sb.String()})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	resp, err := c.doJSONPOSTWithEOFRetry(ctx, c.graphqlURL, body)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graphql status %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			Repository map[string]struct {
				Signature *struct {
					IsValid bool `json:"isValid"`
				} `json:"signature"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if len(result.Errors) > 0 {
		// Partial-data responses are legal in GraphQL; the data field can
		// be populated alongside errors. Log and continue to harvest what
		// did come back.
		c.log.Warn("github: graphql enrich partial errors",
			slog.Int("count", len(result.Errors)),
			slog.String("first", result.Errors[0].Message),
		)
	}

	for i, sha := range shas {
		alias := fmt.Sprintf("a%d", i)
		node, ok := result.Data.Repository[alias]
		if !ok {
			continue
		}
		var en commitEnrichment
		if node.Signature != nil {
			v := node.Signature.IsValid
			en.SignatureVerified = &v
		}
		out[sha] = en
	}
	return nil
}

// graphqlSafe strips characters that could prematurely terminate a string
// literal. Owner/repo names per the config validator are already a subset
// of [A-Za-z0-9._-]; this is belt-and-braces.
func graphqlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '\n', '\r':
			return -1
		}
		return r
	}, s)
}
