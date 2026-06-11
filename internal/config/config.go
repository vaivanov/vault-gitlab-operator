// Package config defines the YAML configuration schema of the operator:
// connection settings for Vault and GitLab, sync behaviour, defaults,
// reusable variable bundles and the sync targets (instance, groups,
// projects). Load parses, validates and expands a config file.
package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root of the YAML configuration file.
type Config struct {
	Vault    VaultConfig               `yaml:"vault"`
	GitLab   GitLabConfig              `yaml:"gitlab"`
	Sync     SyncConfig                `yaml:"sync"`
	Defaults Defaults                  `yaml:"defaults"`
	Bundles  map[string][]VariableSpec `yaml:"bundles"`
	Targets  Targets                   `yaml:"targets"`

	// Warnings collected during validation (non-fatal findings, e.g.
	// environment_scope on a group variable which is Premium-only).
	Warnings []string `yaml:"-"`
	// Expanded is the flattened form consumed by the reconciler:
	// bundles merged, defaults applied. Populated by Load.
	Expanded []Target `yaml:"-"`
}

// VaultConfig configures the connection and authentication to Vault.
type VaultConfig struct {
	Address string         `yaml:"address"`
	TLS     VaultTLS       `yaml:"tls"`
	Auth    VaultAuth      `yaml:"auth"`
}

type VaultTLS struct {
	CACertFile         string `yaml:"ca_cert_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type VaultAuth struct {
	Method     string          `yaml:"method"` // token | approle | kubernetes
	Token      TokenAuth       `yaml:"token"`
	AppRole    AppRoleAuth     `yaml:"approle"`
	Kubernetes KubernetesAuth  `yaml:"kubernetes"`
}

// TokenAuth reads a Vault token from an environment variable or a file.
type TokenAuth struct {
	Env  string `yaml:"env"`
	File string `yaml:"file"`
}

type AppRoleAuth struct {
	Mount        string `yaml:"mount"`
	RoleIDEnv    string `yaml:"role_id_env"`
	RoleIDFile   string `yaml:"role_id_file"`
	SecretIDEnv  string `yaml:"secret_id_env"`
	SecretIDFile string `yaml:"secret_id_file"`
}

type KubernetesAuth struct {
	Mount   string `yaml:"mount"`
	Role    string `yaml:"role"`
	JWTFile string `yaml:"jwt_file"`
}

// GitLabConfig configures the connection to the GitLab instance. The token
// is never stored in the YAML itself — only a reference to an environment
// variable or a file.
type GitLabConfig struct {
	URL       string  `yaml:"url"`
	TokenEnv  string  `yaml:"token_env"`
	TokenFile string  `yaml:"token_file"`
	RateLimit float64 `yaml:"rate_limit"` // requests/sec, 0 = unlimited
}

// SyncConfig controls the reconcile loop.
type SyncConfig struct {
	Interval          Duration `yaml:"interval"`
	Jitter            Duration `yaml:"jitter"`
	Concurrency       int      `yaml:"concurrency"`
	OnMaskedViolation string   `yaml:"on_masked_violation"` // error | skip-warn
}

const (
	MaskedViolationError    = "error"
	MaskedViolationSkipWarn = "skip-warn"
)

// Defaults are merged into every variable spec unless the spec overrides
// the field explicitly. Raw is a pointer because its built-in default is
// true, which a zero value cannot express.
type Defaults struct {
	VaultMount       string `yaml:"vault_mount"`
	VariableType     string `yaml:"variable_type"`
	Protected        bool   `yaml:"protected"`
	Masked           bool   `yaml:"masked"`
	Raw              *bool  `yaml:"raw"`
	EnvironmentScope string `yaml:"environment_scope"`
	Description      string `yaml:"description"`
}

// VariableSpec is one variable mapping as written in YAML, either under a
// bundle or inline under a target. Pointer fields distinguish "not set"
// (inherit from defaults) from an explicit value.
type VariableSpec struct {
	Key        string    `yaml:"key"`
	Vault      *VaultRef `yaml:"vault"`
	FromSecret *VaultRef `yaml:"from_secret"`
	Prefix     string    `yaml:"prefix"`

	VariableType     *string `yaml:"variable_type"` // env_var | file
	Protected        *bool   `yaml:"protected"`
	Masked           *bool   `yaml:"masked"`
	Raw              *bool   `yaml:"raw"`
	EnvironmentScope *string `yaml:"environment_scope"`
	Description      *string `yaml:"description"`
}

// VaultRef addresses a KV v2 secret (and optionally a single field in it).
type VaultRef struct {
	Mount string `yaml:"mount"`
	Path  string `yaml:"path"`
	Field string `yaml:"field"`
}

func (r VaultRef) String() string {
	if r.Field == "" {
		return r.Mount + "/" + r.Path
	}
	return r.Mount + "/" + r.Path + "#" + r.Field
}

// SecretKey identifies the secret (mount+path) independent of field, used
// for deduplicating Vault reads.
func (r VaultRef) SecretKey() string { return r.Mount + "/" + r.Path }

// Targets groups the three GitLab levels a variable can be synced to.
type Targets struct {
	Instance *InstanceTarget `yaml:"instance"`
	Groups   []GroupTarget   `yaml:"groups"`
	Projects []ProjectTarget `yaml:"projects"`
}

type InstanceTarget struct {
	Bundles   []string       `yaml:"bundles"`
	Variables []VariableSpec `yaml:"variables"`
}

type GroupTarget struct {
	Group     string         `yaml:"group"` // path or numeric ID
	Bundles   []string       `yaml:"bundles"`
	Variables []VariableSpec `yaml:"variables"`
}

type ProjectTarget struct {
	Project   string         `yaml:"project"` // path or numeric ID
	Bundles   []string       `yaml:"bundles"`
	Variables []VariableSpec `yaml:"variables"`
}

// --- expanded (post-Load) form -------------------------------------------

type TargetKind string

const (
	KindInstance TargetKind = "instance"
	KindGroup    TargetKind = "group"
	KindProject  TargetKind = "project"
)

// TargetRef identifies one GitLab sync destination. ID is resolved from
// Ref lazily by the GitLab layer (0 until then; always 0 for instance).
type TargetRef struct {
	Kind TargetKind
	Ref  string
	ID   int
}

func (t TargetRef) String() string {
	if t.Kind == KindInstance {
		return "instance"
	}
	return string(t.Kind) + ":" + t.Ref
}

// Target is a destination with its fully resolved variable specs.
type Target struct {
	Ref       TargetRef
	Variables []ResolvedVariable
}

// ResolvedVariable is a VariableSpec after bundle merging and defaults
// application: every attribute carries a concrete value.
type ResolvedVariable struct {
	Key        string
	Vault      *VaultRef // exactly one of Vault / FromSecret is non-nil
	FromSecret *VaultRef
	Prefix     string

	Type             string // env_var | file
	Protected        bool
	Masked           bool
	Raw              bool
	EnvironmentScope string
	Description      string
}

// Identity is the GitLab-side identity of a variable within one target.
type Identity struct {
	Key              string
	EnvironmentScope string
}

func (v ResolvedVariable) Identity() Identity {
	return Identity{Key: v.Key, EnvironmentScope: v.EnvironmentScope}
}

// Duration wraps time.Duration with YAML support for strings like "5m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\" or \"5m\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }
