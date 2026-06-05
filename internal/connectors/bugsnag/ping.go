package bugsnag

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Ping verifies the configured token by calling GET /user. 401 is a fatal
// authentication failure; other non-2xx responses are surfaced verbatim so
// the caller can log the upstream status.
func (c *Connector) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/user", nil)
	if err != nil {
		return err
	}
	c.authHeader(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("bugsnag: 401 token rejected")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bugsnag: ping returned %d", resp.StatusCode)
	}
	return nil
}
