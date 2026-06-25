package honeycomb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// markerURLRe matches GitHub commit URLs produced by honeymarker:
//
//	https://github.com/<owner>/<repo>/commits/<sha40>
//
// Capture group 1 is the "owner/repo" slug; group 2 is the 40-char hex SHA.
var markerURLRe = regexp.MustCompile(
	`github\.com/([\w.\-]+/[\w.\-]+)/commits/([0-9a-f]{40})(?:[/?#]|$)`,
)

// repoFromMarkerURL extracts "owner/repo" and the 40-char commit SHA from a
// GitHub commit URL like https://github.com/owner/repo/commits/<sha40>.
// Returns ("", "") if the URL doesn't match the expected pattern.
func repoFromMarkerURL(rawURL string) (slug, sha string) {
	m := markerURLRe.FindStringSubmatch(rawURL)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// marker is the slice of Honeycomb's marker payload xray cares about.
//
// See https://docs.honeycomb.io/api/tag/Markers — start_time/end_time are
// epoch seconds in the wire format, decoded as int64 and converted to UTC
// time.Time on read.
type marker struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	StartTime  int64  `json:"start_time"`
	EndTime    int64  `json:"end_time"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	Color      string `json:"color"`
	DatasetTag string `json:"dataset"`
}

// listMarkers fetches all markers for the configured dataset. The endpoint
// returns a flat array with no server-side date filter, so all markers are
// fetched and filtered client-side in extractDeploys. Volume grows with
// deployment frequency; no pagination to manage.
//
// Results are memoised in-process via sync.Once so that concurrent Extract
// calls for different repos share a single fetch rather than issuing one
// HTTP request (or disk read) per repo. Within the once, the on-disk cache
// is consulted first; a live HTTP fetch is made only on a cache miss.
func (c *Connector) listMarkers(ctx context.Context) ([]marker, error) {
	c.once.Do(func() {
		c.memoMarkers, c.memoErr = c.fetchMarkers(ctx)
	})
	return c.memoMarkers, c.memoErr
}

// fetchMarkers is the single-fetch implementation backing listMarkers. It
// checks the on-disk cache first and falls back to an HTTP request on a miss.
func (c *Connector) fetchMarkers(ctx context.Context) ([]marker, error) {
	var cacheFile string
	if !c.noCache {
		fp := cacheFingerprint(c.token, c.dataset, c.baseURL)
		if path, err := cachePath(fp); err == nil {
			cacheFile = path
			if markers, fresh := readMarkerCache(path); fresh {
				c.log.Debug("honeycomb: marker cache hit", "dataset", c.dataset)
				return markers, nil
			}
		}
	}

	u := c.baseURL + "/markers/" + url.PathEscape(c.dataset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("honeycomb: 401 listing markers")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("honeycomb: list markers %s: %d", c.dataset, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []marker
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}

	if cacheFile != "" {
		writeMarkerCache(cacheFile, out, c.log)
	}
	return out, nil
}

// canonicalEnvironments is the set of valid deploys.environment values.
var canonicalEnvironments = map[string]string{
	"production": "production",
	"prod":       "production",
	"staging":    "staging",
	"stage":      "staging",
	"preview":    "preview",
	"release":    "release",
	"other":      "other",
}

// resolveEnvironment returns the canonical deploys.environment value for a
// Honeycomb marker. When cfgEnvironment is non-empty (operator-declared via
// TOML) it is returned verbatim — config wins. Otherwise markerType is looked
// up in the synonym map; unknown strings (including the common "deploy") map
// to "other".
func resolveEnvironment(cfgEnvironment, markerType string) string {
	if cfgEnvironment != "" {
		return cfgEnvironment
	}
	if canon, ok := canonicalEnvironments[markerType]; ok {
		return canon
	}
	return "other"
}

// markerToDeploy maps a Honeycomb marker to a canonical model.Deploy
// attributed to the supplied repo slug and commit SHA. cfgEnvironment is the
// operator-declared environment from TOML; when non-empty it takes precedence
// over the marker type string. Pure function: kept separate from HTTP
// plumbing so the mapping can be unit-tested without a fake server.
func markerToDeploy(m marker, repoSlug, commitSHA, cfgEnvironment string) model.Deploy {
	return model.Deploy{
		ID:                 m.ID,
		Repo:               repoSlug,
		Environment:        resolveEnvironment(cfgEnvironment, m.Type),
		DeployedAt:         time.Unix(m.StartTime, 0).UTC(),
		CommitSHA:          commitSHA,
		Source:             "honeycomb",
		Status:             "success",
		SupersedesDeployID: "",
		RolledBack:         false,
		Trigger:            "",
		ReleaseTag:         "",
		Version:            m.Message,
	}
}

// extractDeploys lists markers, filters to the window, and emits one Deploy
// per in-window marker. Returns (rowsEmitted, paginationComplete, error) where
// error is non-nil only for the list-call / context failures. Per-row insert
// failures are recorded into prov.Errors with per-id keys and do not abort
// the walk.
func (c *Connector) extractDeploys(ctx context.Context, repoSlug string, window connector.Window, sink connector.Sink, prov *connector.Provenance) (int, bool, error) {
	markers, err := c.listMarkers(ctx)
	if err != nil {
		return 0, false, err
	}

	rows := 0
	for _, m := range markers {
		if err := ctx.Err(); err != nil {
			return rows, false, err
		}
		if m.StartTime == 0 {
			continue
		}
		t := time.Unix(m.StartTime, 0).UTC()
		if !window.Contains(t) {
			continue
		}

		// Attribute each marker to its repo via the URL. Skip markers whose
		// URL resolves to a different repo so we don't double-emit under every
		// repo that runs Extract. Markers with no URL or a non-GitHub URL are
		// skipped and logged at debug level.
		slug, sha := repoFromMarkerURL(m.URL)
		if slug != repoSlug {
			if m.URL == "" {
				c.log.Debug("honeycomb: marker has no URL; skipping", "marker_id", m.ID, "repo", repoSlug)
			} else {
				c.log.Debug("honeycomb: marker URL does not match repo; skipping",
					"marker_id", m.ID, "url", m.URL, "repo", repoSlug)
			}
			continue
		}

		d := markerToDeploy(m, repoSlug, sha, c.environment)
		if err := sink.InsertDeploy(d); err != nil {
			prov.Errors["deploys:"+m.ID] = err.Error()
			continue
		}
		rows++
	}
	return rows, true, nil
}
