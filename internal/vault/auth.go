package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/auth/approle"
	"github.com/hashicorp/vault/api/auth/kubernetes"
)

// Login authenticates the client using the configured method and stores
// the resulting auth secret for renewal via KeepAlive.
func (c *Client) Login(ctx context.Context) error {
	switch c.cfg.Auth.Method {
	case "token":
		token, err := c.cfg.Auth.Token.Token()
		if err != nil {
			return fmt.Errorf("vault auth: %w", err)
		}
		c.api.SetToken(token.Reveal())
		c.setLoginSecret(nil) // static token: nothing to renew
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
	roleID, secretID, err := c.cfg.Auth.AppRole.Credentials()
	if err != nil {
		return fmt.Errorf("vault auth: %w", err)
	}
	auth, err := approle.NewAppRoleAuth(
		roleID,
		&approle.SecretID{FromString: secretID.Reveal()},
		approle.WithMountPath(c.cfg.Auth.AppRole.Mount),
	)
	if err != nil {
		return fmt.Errorf("vault auth: approle: %w", err)
	}
	secret, err := c.api.Auth().Login(ctx, auth)
	if err != nil {
		return fmt.Errorf("vault auth: approle login: %w", err)
	}
	c.setLoginSecret(secret)
	return nil
}

func (c *Client) loginKubernetes(ctx context.Context) error {
	auth, err := kubernetes.NewKubernetesAuth(
		c.cfg.Auth.Kubernetes.Role,
		kubernetes.WithServiceAccountTokenPath(c.cfg.Auth.Kubernetes.JWTFile),
		kubernetes.WithMountPath(c.cfg.Auth.Kubernetes.Mount),
	)
	if err != nil {
		return fmt.Errorf("vault auth: kubernetes: %w", err)
	}
	secret, err := c.api.Auth().Login(ctx, auth)
	if err != nil {
		return fmt.Errorf("vault auth: kubernetes login: %w", err)
	}
	c.setLoginSecret(secret)
	return nil
}

func (c *Client) setLoginSecret(s *api.Secret) {
	c.mu.Lock()
	c.loginSecret = s
	c.mu.Unlock()
}

func (c *Client) getLoginSecret() *api.Secret {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loginSecret
}

// KeepAlive renews the auth token in the background until ctx is
// cancelled. Renewable tokens are renewed via a LifetimeWatcher; when a
// token reaches its max TTL or renewal fails, KeepAlive re-logins with
// exponential backoff. Static tokens (method "token") need no renewal,
// so KeepAlive returns immediately for them.
func (c *Client) KeepAlive(ctx context.Context, log *slog.Logger) {
	if c.cfg.Auth.Method == "token" {
		return
	}

	backoff := time.Second
	for ctx.Err() == nil {
		secret := c.getLoginSecret()
		if secret == nil || secret.Auth == nil || !secret.Auth.Renewable {
			// Nothing renewable: wait out most of the TTL, then re-login.
			ttl := time.Minute
			if secret != nil && secret.Auth != nil && secret.Auth.LeaseDuration > 0 {
				ttl = time.Duration(secret.Auth.LeaseDuration) * time.Second * 2 / 3
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(ttl):
			}
		} else if err := c.watchToken(ctx, secret, log); err == nil {
			// Watcher ended because ctx was cancelled.
			return
		}

		// Re-login until it succeeds.
		for ctx.Err() == nil {
			if err := c.Login(ctx); err != nil {
				log.Error("vault re-login failed", "error", err, "retry_in", backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff = min(backoff*2, time.Minute)
				continue
			}
			log.Info("vault re-login succeeded")
			backoff = time.Second
			break
		}
	}
}

// watchToken renews the token until it cannot be renewed any further.
// Returns nil when ctx ended, an error when renewal stopped and a
// re-login is required.
func (c *Client) watchToken(ctx context.Context, secret *api.Secret, log *slog.Logger) error {
	watcher, err := c.api.NewLifetimeWatcher(&api.LifetimeWatcherInput{Secret: secret})
	if err != nil {
		return fmt.Errorf("create lifetime watcher: %w", err)
	}
	go watcher.Start()
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-watcher.DoneCh():
			if err != nil {
				log.Warn("vault token renewal stopped", "error", err)
				return err
			}
			return errors.New("vault token reached max TTL")
		case renewal := <-watcher.RenewCh():
			log.Debug("vault token renewed", "lease_duration", renewal.Secret.Auth.LeaseDuration)
		}
	}
}
