package github

import (
	"context"
	"net/http"
	"strings"

	"github.com/kmcd/xray/internal/connector"
)

// template captures the section headings extracted from a repo's
// PULL_REQUEST_TEMPLATE.md. score() then measures the conformance of an
// individual PR body against those headings as a 0-1 float.
type template struct {
	headings []string // lowercased, trimmed of leading '#' and surrounding space.
}

// fetchTemplate loads .github/PULL_REQUEST_TEMPLATE.md for the given repo
// and returns its parsed headings. The result is cached per slug. Returns
// (nil, nil) when the template is absent or when the endpoint is forbidden
// (403); (nil, err) on other errors so the caller can log them.
//
// prov.Endpoints["pr_template"] is written on every API-touching path:
//   - 404 (file absent) -> Accessible: true (endpoint reachable, file not there)
//   - 403 (token lacks scope) -> Accessible: false
//   - other err (5xx/network) -> Accessible: false; err bubbled
//   - success -> Accessible: true
func (c *Connector) fetchTemplate(ctx context.Context, slug string, prov *connector.Provenance) (*template, error) {
	c.mu.Lock()
	if t, ok := c.templateCache[slug]; ok {
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	owner, name, ok := splitSlug(slug)
	if !ok {
		return nil, nil
	}
	file, _, resp, err := c.rest.Repositories.GetContents(ctx, owner, name, ".github/PULL_REQUEST_TEMPLATE.md", nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			prov.Endpoints["pr_template"] = connector.EndpointStatus{Accessible: true}
			c.mu.Lock()
			c.templateCache[slug] = nil
			c.mu.Unlock()
			return nil, nil
		}
		prov.Endpoints["pr_template"] = connector.EndpointStatus{
			Accessible: false,
			Reason:     err.Error(),
		}
		if resp != nil && resp.StatusCode == http.StatusForbidden {
			c.mu.Lock()
			c.templateCache[slug] = nil
			c.mu.Unlock()
			return nil, nil
		}
		return nil, err
	}
	prov.Endpoints["pr_template"] = connector.EndpointStatus{Accessible: true}
	if file == nil {
		c.mu.Lock()
		c.templateCache[slug] = nil
		c.mu.Unlock()
		return nil, nil
	}
	content, err := file.GetContent()
	if err != nil {
		return nil, err
	}
	t := parseTemplate(content)
	c.mu.Lock()
	c.templateCache[slug] = t
	c.mu.Unlock()
	return t, nil
}

// parseTemplate extracts markdown headings from a template body. A
// heading line starts with one or more '#' characters followed by a space.
func parseTemplate(content string) *template {
	var t template
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		// Strip leading '#' chars and any following whitespace.
		stripped := strings.TrimLeft(line, "#")
		stripped = strings.TrimSpace(stripped)
		if stripped == "" {
			continue
		}
		t.headings = append(t.headings, strings.ToLower(stripped))
	}
	if len(t.headings) == 0 {
		return nil
	}
	return &t
}

// score returns the fraction of template headings that appear in the PR
// body as substrings (case-insensitive). Returns 0 when the template has
// no headings.
func (t *template) score(body string) float64 {
	if t == nil || len(t.headings) == 0 {
		return 0
	}
	low := strings.ToLower(body)
	hits := 0
	for _, h := range t.headings {
		if strings.Contains(low, h) {
			hits++
		}
	}
	return float64(hits) / float64(len(t.headings))
}
