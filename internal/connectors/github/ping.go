package github

import "context"

// Ping performs a read-only authentication check against the GitHub REST
// API. Used by `xray check` to verify the token works without writing
// anything.
func (c *Connector) Ping(ctx context.Context) error {
	_, _, err := c.rest.Users.Get(ctx, "")
	return err
}
