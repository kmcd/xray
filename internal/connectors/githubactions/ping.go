package githubactions

import "context"

// Ping verifies the token by calling the authenticated-user endpoint. Same
// API host as the github connector, so a successful response here implies the
// configured token is usable for the Actions and Deployments APIs.
func (c *Connector) Ping(ctx context.Context) error {
	_, _, err := c.client.Users.Get(ctx, "")
	return err
}
