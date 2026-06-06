package sentry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kmcd/xray/internal/connector"
	"github.com/kmcd/xray/internal/model"
)

// issue mirrors the relevant subset of Sentry's issues list payload. Unused
// fields are dropped; new fields can be added without breaking decoding.
type issue struct {
	ID            string         `json:"id"`
	Status        string         `json:"status"`
	Level         string         `json:"level"`
	Culprit       string         `json:"culprit"`
	Message       string         `json:"message"`
	Title         string         `json:"title"`
	Count         string         `json:"count"`
	FirstSeen     string         `json:"firstSeen"`
	LastSeen      string         `json:"lastSeen"`
	IsUnhandled   bool           `json:"isUnhandled"`
	FirstRelease  *issueRelease  `json:"firstRelease"`
	Tags          []issueTag     `json:"tags"`
}

type issueRelease struct {
	Version      string `json:"version"`
	ShortVersion string `json:"shortVersion"`
}

type issueTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// listIssues pages through /projects/{org}/{project-slug}/issues/ following
// the `rel="next"` Link header until the API signals no more results. The
// returned slice contains every issue Sentry exposed for the query;
// window filtering happens during mapping.
//
// The returned bool reports pagination completeness. It is false when a
// page request fails after exhausting the helper's retry budget.
func (c *Connector) listIssues(ctx context.Context, sentrySlug string, window connector.Window) ([]issue, bool, error) {
	stats := statsPeriod(window)

	base := fmt.Sprintf("%s/projects/%s/%s/issues/", c.baseURL, c.org, sentrySlug)
	q := url.Values{}
	q.Set("query", "is:unresolved OR is:resolved")
	q.Set("statsPeriod", stats)
	q.Set("limit", "100")
	next := base + "?" + q.Encode()

	var all []issue
	for next != "" {
		page, link, err := c.fetchIssuesPage(ctx, next)
		if err != nil {
			return all, false, err
		}
		all = append(all, page...)

		nextURL, hasNext := parseNextLink(link)
		if !hasNext {
			break
		}
		next = nextURL
	}
	return all, true, nil
}

func (c *Connector) fetchIssuesPage(ctx context.Context, u string) ([]issue, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", fmt.Errorf("sentry: build issues request: %w", err)
	}
	c.authHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("sentry: issues: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, "", fmt.Errorf("sentry: 401 — token rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("sentry: issues unexpected status %d", resp.StatusCode)
	}

	var page []issue
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("sentry: decode issues: %w", err)
	}
	return page, resp.Header.Get("Link"), nil
}

// statsPeriod renders the configured window as Sentry's compact relative
// statsPeriod token. We round up to whole days so the boundary issue is
// included even when the window starts mid-day. The minimum is 1d, which
// matches Sentry's accepted granularity.
func statsPeriod(w connector.Window) string {
	d := time.Since(w.Start)
	if d <= 0 {
		return "1d"
	}
	days := int(d.Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	return fmt.Sprintf("%dd", days)
}

// linkRel parses a single Link header segment such as
//
//	<https://sentry.io/api/0/...?cursor=abc>; rel="next"; results="true"; cursor="abc"
//
// returning the URL, the rel value, and a results=true flag. Sentry sets
// results="false" on the trailing next link of the final page; we treat
// that as "no more results" to avoid an extra empty fetch.
var linkSegmentRE = regexp.MustCompile(`<([^>]+)>;\s*(.*)`)

func parseNextLink(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	for _, raw := range splitLinkHeader(header) {
		m := linkSegmentRE.FindStringSubmatch(raw)
		if len(m) != 3 {
			continue
		}
		urlStr := m[1]
		params := m[2]
		if !linkParamEquals(params, "rel", "next") {
			continue
		}
		// Sentry-specific: results="false" means this next link is empty.
		if linkParamEquals(params, "results", "false") {
			return "", false
		}
		return urlStr, true
	}
	return "", false
}

// splitLinkHeader splits a Link header on commas that separate segments
// without disturbing commas inside angle brackets. Sentry URLs do not
// contain commas in practice, but the safer split is cheap.
func splitLinkHeader(h string) []string {
	var out []string
	depth := 0
	last := 0
	for i, r := range h {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(h[last:i]))
				last = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(h[last:]))
	return out
}

func linkParamEquals(params, key, want string) bool {
	for _, p := range strings.Split(params, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(p[:eq])
		v := strings.Trim(strings.TrimSpace(p[eq+1:]), `"`)
		if k == key && v == want {
			return true
		}
	}
	return false
}

// mapIssue projects a Sentry issue onto a canonical Incident row. Returns
// (zero, false) when the issue's firstSeen is outside the window or is not
// parseable, so the caller can skip without erroring out the whole page.
func mapIssue(iss issue, repoSlug string, window connector.Window) (model.Incident, bool) {
	opened, err := parseSentryTime(iss.FirstSeen)
	if err != nil {
		return model.Incident{}, false
	}
	if !window.Contains(opened) {
		return model.Incident{}, false
	}

	var resolved *time.Time
	if iss.Status == "resolved" {
		if t, err := parseSentryTime(iss.LastSeen); err == nil {
			resolved = &t
		}
	}

	occ := 0
	if iss.Count != "" {
		if n, err := strconv.Atoi(iss.Count); err == nil {
			occ = n
		}
	}

	release := ""
	if iss.FirstRelease != nil {
		release = iss.FirstRelease.ShortVersion
		if release == "" {
			release = iss.FirstRelease.Version
		}
	}

	// is_regression for Sentry is sourced solely from issue.isUnhandled
	// per ADR 018. The previous heuristic OR'd in a substring match for
	// "regression" across message/title/culprit/tag values, but that
	// conflates user-named tags (e.g. a team labelling errors
	// "regression-candidate") with source-level state and would flood the
	// column with false positives. Bugsnag keeps its own per-source rule
	// (reopened_at != nil); the two definitions are intentionally distinct.
	return model.Incident{
		ID:             iss.ID,
		Repo:           repoSlug,
		Source:         "sentry",
		OpenedAt:       opened,
		ResolvedAt:     resolved,
		Severity:       iss.Level,
		Occurrences:    occ,
		ReleaseRef:     release,
		DeployID:       "",
		CommitSHA:      "",
		AcknowledgedAt: nil,
		IsRegression:   iss.IsUnhandled,
		CulpritRef:     iss.Culprit,
	}, true
}

// parseSentryTime accepts the ISO-8601 timestamps Sentry emits. Sentry
// occasionally returns timestamps without an explicit zone; we fall back
// to RFC3339Nano, RFC3339, and an explicit UTC layout.
func parseSentryTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000000Z",
		"2006-01-02T15:04:05Z",
	}
	var lastErr error
	for _, l := range layouts {
		t, err := time.Parse(l, s)
		if err == nil {
			return t.UTC(), nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}
