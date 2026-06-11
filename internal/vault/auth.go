package vault

import (
	"context"
	"fmt"
)

// Login authenticates the client using the configured method. For the
// static token method it simply installs the token; AppRole and
// Kubernetes login (with renewal) are wired in the daemon milestone.
func (c *Client) Login(ctx context.Context) error {
	switch c.cfg.Auth.Method {
	case "token":
		token, err := c.cfg.Auth.Token.Token()
		if err != nil {
			return fmt.Errorf("vault auth: %w", err)
		}
		c.api.SetToken(token.Reveal())
		return nil
	case "approle":
		return c.loginAppRole(ctx)
	case "kubernetes":
		return c.loginKubernetes(ctx)
	default:
		return fmt.Errorf("vault auth: unsupported method %q", c.cfg.Auth.Method)
	}
}

func (c *Client) loginAppRole(ctx context.Context) error {
	return fmt.Errorf("vault auth: approle login not implemented yet")
}

func (c *Client) loginKubernetes(ctx context.Context) error {
	return fmt.Errorf("vault auth: kubernetes login not implemented yet")
}
