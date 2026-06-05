package sentry

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Ping performs a read-only authentication check against the configured
// Sentry organisation. Used by `xray check`. A 401 is fatal; the token is
// rejected outright. Other non-2xx responses are surfaced verbatim.
func (c *Connector) Ping(ctx context.Context) error {
	url := fmt.Sprintf("%s/organizations/%s/", c.baseURL, c.org)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("sentry: build ping request: %w", err)
	}
	c.authHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sentry: ping: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("sentry: 401 — token rejected")
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	default:
		return fmt.Errorf("sentry: ping unexpected status %d", resp.StatusCode)
	}
}
