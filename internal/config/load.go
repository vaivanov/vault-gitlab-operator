package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vaivanov/vault-gitlab-operator/internal/logging"
)

// Built-in defaults applied before validation when the config omits them.
const (
	defaultVaultMount       = "secret"
	defaultVariableType     = "env_var"
	defaultEnvironmentScope = "*"
	defaultDescription      = "Managed by vault-gitlab-operator"
	defaultInterval         = 5 * time.Minute
	defaultConcurrency      = 4
	defaultAppRoleMount     = "approle"
	defaultKubernetesMount  = "kubernetes"
	defaultKubernetesJWT    = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

// Load reads, parses, validates and expands the config file at path.
// Unknown YAML keys are rejected. Returned errors aggregate every
// validation failure found, not just the first.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse is Load for in-memory config bytes (used by tests and SIGHUP reload).
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	expanded, err := cfg.expand()
	if err != nil {
		return nil, err
	}
	cfg.Expanded = expanded
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Vault.Address == "" {
		c.Vault.Address = os.Getenv("VAULT_ADDR")
	}
	if c.Vault.Auth.Method == "" {
		c.Vault.Auth.Method = "token"
	}
	if c.Vault.Auth.Method == "token" && c.Vault.Auth.Token.Env == "" && c.Vault.Auth.Token.File == "" {
		c.Vault.Auth.Token.Env = "VAULT_TOKEN"
	}
	if c.Vault.Auth.AppRole.Mount == "" {
		c.Vault.Auth.AppRole.Mount = defaultAppRoleMount
	}
	if c.Vault.Auth.Kubernetes.Mount == "" {
		c.Vault.Auth.Kubernetes.Mount = defaultKubernetesMount
	}
	if c.Vault.Auth.Kubernetes.JWTFile == "" {
		c.Vault.Auth.Kubernetes.JWTFile = defaultKubernetesJWT
	}

	if c.Sync.Interval == 0 {
		c.Sync.Interval = Duration(defaultInterval)
	}
	if c.Sync.Concurrency == 0 {
		c.Sync.Concurrency = defaultConcurrency
	}
	if c.Sync.OnMaskedViolation == "" {
		c.Sync.OnMaskedViolation = MaskedViolationError
	}

	if c.Defaults.VaultMount == "" {
		c.Defaults.VaultMount = defaultVaultMount
	}
	if c.Defaults.VariableType == "" {
		c.Defaults.VariableType = defaultVariableType
	}
	if c.Defaults.Raw == nil {
		// GitLab 18.6 flipped the API default for `raw`; we always send it
		// explicitly and default to true (no variable expansion).
		raw := true
		c.Defaults.Raw = &raw
	}
	if c.Defaults.EnvironmentScope == "" {
		c.Defaults.EnvironmentScope = defaultEnvironmentScope
	}
	if c.Defaults.Description == "" {
		c.Defaults.Description = defaultDescription
	}
}

// Token resolves the GitLab API token from the configured env var or
// file reference.
func (c *GitLabConfig) Token() (logging.Secret, error) {
	return resolveSecret("gitlab token", c.TokenEnv, c.TokenFile)
}

// Token resolves the static Vault token (auth method "token").
func (a *TokenAuth) Token() (logging.Secret, error) {
	return resolveSecret("vault token", a.Env, a.File)
}

// Credentials resolves the AppRole role_id and secret_id.
func (a *AppRoleAuth) Credentials() (roleID string, secretID logging.Secret, err error) {
	role, err := resolveSecret("approle role_id", a.RoleIDEnv, a.RoleIDFile)
	if err != nil {
		return "", "", err
	}
	secretID, err = resolveSecret("approle secret_id", a.SecretIDEnv, a.SecretIDFile)
	if err != nil {
		return "", "", err
	}
	return role.Reveal(), secretID, nil
}

func resolveSecret(what, envName, fileName string) (logging.Secret, error) {
	switch {
	case envName != "":
		v := os.Getenv(envName)
		if v == "" {
			return "", fmt.Errorf("%s: environment variable %s is empty or unset", what, envName)
		}
		return logging.Secret(v), nil
	case fileName != "":
		b, err := os.ReadFile(fileName)
		if err != nil {
			return "", fmt.Errorf("%s: %w", what, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("%s: file %s is empty", what, fileName)
		}
		return logging.Secret(v), nil
	default:
		return "", fmt.Errorf("%s: neither env nor file reference configured", what)
	}
}
