// Package vault reads KV v2 secrets through the Vault HTTP API. Change
// detection relies on the cheap metadata endpoint (current_version), so
// secret data is only fetched when a version actually changed.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/vault/api"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

// ErrNotFound is returned when a secret (or its metadata) does not exist.
var ErrNotFound = errors.New("secret not found")

// SecretSource reads KV v2 secrets. Version is a metadata-only call;
// Read returns the full data of the current version.
type SecretSource interface {
	Version(ctx context.Context, ref config.VaultRef) (int, time.Time, error)
	Read(ctx context.Context, ref config.VaultRef) (data map[string]string, version int, err error)
}

// Client implements SecretSource over the Vault API.
type Client struct {
	api *api.Client
	cfg config.VaultConfig

	mu          sync.Mutex
	loginSecret *api.Secret // last auth response, renewed by KeepAlive
}

// New builds a Client from config. Call Login before use.
func New(cfg config.VaultConfig) (*Client, error) {
	apiCfg := api.DefaultConfig()
	apiCfg.Address = cfg.Address
	apiCfg.Timeout = 10 * time.Second
	if cfg.TLS.CACertFile != "" || cfg.TLS.InsecureSkipVerify {
		tls := &api.TLSConfig{
			CACert:   cfg.TLS.CACertFile,
			Insecure: cfg.TLS.InsecureSkipVerify,
		}
		if err := apiCfg.ConfigureTLS(tls); err != nil {
			return nil, fmt.Errorf("configure vault TLS: %w", err)
		}
	}
	c, err := api.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("create vault client: %w", err)
	}
	return &Client{api: c, cfg: cfg}, nil
}

// Version reads KV v2 metadata and returns current_version and updated_time.
func (c *Client) Version(ctx context.Context, ref config.VaultRef) (int, time.Time, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, ref.Mount+"/metadata/"+ref.Path)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("read metadata %s: %w", ref.SecretKey(), err)
	}
	if secret == nil || secret.Data == nil {
		return 0, time.Time{}, fmt.Errorf("metadata %s: %w", ref.SecretKey(), ErrNotFound)
	}

	version, err := toInt(secret.Data["current_version"])
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("metadata %s: bad current_version: %w", ref.SecretKey(), err)
	}
	var updated time.Time
	if s, ok := secret.Data["updated_time"].(string); ok {
		updated, _ = time.Parse(time.RFC3339, s)
	}
	return version, updated, nil
}

// Read fetches the current version of the secret data. Non-string field
// values are stringified (numbers/bools via fmt, structures as JSON).
func (c *Client) Read(ctx context.Context, ref config.VaultRef) (map[string]string, int, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, ref.Mount+"/data/"+ref.Path)
	if err != nil {
		return nil, 0, fmt.Errorf("read secret %s: %w", ref.SecretKey(), err)
	}
	if secret == nil || secret.Data == nil {
		return nil, 0, fmt.Errorf("secret %s: %w", ref.SecretKey(), ErrNotFound)
	}

	raw, ok := secret.Data["data"].(map[string]any)
	if !ok || raw == nil {
		// KV v2 returns data: null for deleted/destroyed versions.
		return nil, 0, fmt.Errorf("secret %s: current version is deleted: %w", ref.SecretKey(), ErrNotFound)
	}

	version := 0
	if meta, ok := secret.Data["metadata"].(map[string]any); ok {
		if v, err := toInt(meta["version"]); err == nil {
			version = v
		}
	}

	data := make(map[string]string, len(raw))
	for k, v := range raw {
		s, err := stringify(v)
		if err != nil {
			return nil, 0, fmt.Errorf("secret %s: field %q: %w", ref.SecretKey(), k, err)
		}
		data[k] = s
	}
	return data, version, nil
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return int(i), err
	case float64:
		return int(n), nil
	case int:
		return n, nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

func stringify(v any) (string, error) {
	switch s := v.(type) {
	case string:
		return s, nil
	case nil:
		return "", nil
	case bool, json.Number, float64, int:
		return fmt.Sprint(s), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
