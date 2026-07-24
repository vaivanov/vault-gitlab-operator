package config

import (
	"strings"
	"testing"
	"time"
)

// base returns a minimal valid config body; tests mutate pieces of it.
const validBase = `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  projects:
    - project: a/b
      variables:
        - key: K
          vault: {path: p, field: f}
`

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring of the aggregated error
	}{
		{
			name: "missing vault address",
			yaml: `
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "vault.address is required",
		},
		{
			name: "bad vault address scheme",
			yaml: `
vault: {address: "ftp://v:8200"}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "must use http or https",
		},
		{
			name: "gitlab url trailing slash",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: "https://g/", token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "must not end with a slash",
		},
		{
			name: "missing gitlab token ref",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "gitlab token",
		},
		{
			name: "both gitlab token refs",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T, token_file: /tmp/t}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "not both",
		},
		{
			name: "negative rate limit",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T, rate_limit: -1}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "rate_limit",
		},
		{
			name: "unknown auth method",
			yaml: `
vault: {address: https://v:8200, auth: {method: ldap}}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "vault.auth.method",
		},
		{
			name: "approle missing secret_id",
			yaml: `
vault: {address: https://v:8200, auth: {method: approle, approle: {role_id_env: R}}}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "approle.secret_id",
		},
		{
			name: "kubernetes missing role",
			yaml: `
vault: {address: https://v:8200, auth: {method: kubernetes}}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "kubernetes.role",
		},
		{
			name: "bad on_masked_violation",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
sync: {on_masked_violation: explode}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "on_masked_violation",
		},
		{
			name: "zero concurrency",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
sync: {concurrency: -2}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "concurrency",
		},
		{
			name: "bad defaults variable_type",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
defaults: {variable_type: blob}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "defaults.variable_type",
		},
		{
			name: "no targets",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {}`,
			wantErr: "at least one of instance, groups or projects",
		},
		{
			name: "negative pass timeout",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
sync: {timeout: -1s}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "sync.timeout must be >= 0",
		},
		{
			name: "duplicate project target",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  projects:
    - {project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}
    - {project: a/b, variables: [{key: J, vault: {path: q, field: f}}]}`,
			wantErr: `project "a/b" is already declared at targets.projects[0]`,
		},
		{
			name: "duplicate group target",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  groups:
    - {group: platform, variables: [{key: K, vault: {path: p, field: f}}]}
    - {group: platform, variables: [{key: J, vault: {path: q, field: f}}]}`,
			wantErr: `group "platform" is already declared at targets.groups[0]`,
		},
		{
			name: "invalid key characters",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: "BAD-KEY", vault: {path: p, field: f}}]}]}`,
			wantErr: "invalid",
		},
		{
			name: "missing key",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{vault: {path: p, field: f}}]}]}`,
			wantErr: "key is required",
		},
		{
			name: "both vault and from_secret",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}, from_secret: {path: q}}]}]}`,
			wantErr: "mutually exclusive",
		},
		{
			name: "neither vault nor from_secret",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K}]}]}`,
			wantErr: "exactly one of vault or from_secret",
		},
		{
			name: "vault without field",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p}}]}]}`,
			wantErr: "vault.field is required",
		},
		{
			name: "from_secret with field",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{from_secret: {path: p, field: f}}]}]}`,
			wantErr: "from_secret.field is not allowed",
		},
		{
			name: "from_secret with key",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, from_secret: {path: p}}]}]}`,
			wantErr: "key is not allowed with from_secret",
		},
		{
			name: "prefix on single-field spec",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, prefix: APP_, vault: {path: p, field: f}}]}]}`,
			wantErr: "prefix is only valid with from_secret",
		},
		{
			name: "invalid prefix",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{from_secret: {path: p}, prefix: "BAD-"}]}]}`,
			wantErr: "prefix",
		},
		{
			name: "bad spec variable_type",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}, variable_type: blob}]}]}`,
			wantErr: "variable_type must be env_var or file",
		},
		{
			name: "environment_scope on instance",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {instance: {variables: [{key: K, vault: {path: p, field: f}, environment_scope: prod}]}}`,
			wantErr: "environment_scope is not supported on instance",
		},
		{
			name: "unknown bundle reference",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{project: a/b, bundles: [nope], variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: `unknown bundle "nope"`,
		},
		{
			name: "group without ref",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {groups: [{variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "group is required",
		},
		{
			name: "project without ref",
			yaml: `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets: {projects: [{variables: [{key: K, vault: {path: p, field: f}}]}]}`,
			wantErr: "project is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidationAggregatesErrors(t *testing.T) {
	_, err := Parse([]byte(`
gitlab: {url: https://g}
targets: {}`))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"vault.address", "gitlab token", "at least one of"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error missing %q: %v", want, err)
		}
	}
}

func TestGroupScopeWarning(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  groups:
    - group: platform
      variables:
        - key: K
          vault: {path: p, field: f}
          environment_scope: prod
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "Premium") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Premium warning for group environment_scope, warnings = %v", cfg.Warnings)
	}
}

func TestValidBaseParses(t *testing.T) {
	if _, err := Parse([]byte(validBase)); err != nil {
		t.Fatalf("base config should be valid: %v", err)
	}
}

func TestSameNameAcrossLevelsIsNotADuplicate(t *testing.T) {
	// A group and a project may legitimately share a path segment; only
	// duplicates within one level denote the same GitLab object.
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  groups:
    - {group: platform, variables: [{key: K, vault: {path: p, field: f}}]}
  projects:
    - {project: platform, variables: [{key: K, vault: {path: p, field: f}}]}
`))
	if err != nil {
		t.Fatalf("same name at different levels rejected: %v", err)
	}
	if len(cfg.Expanded) != 2 {
		t.Errorf("expanded = %d targets, want 2", len(cfg.Expanded))
	}
}

func TestPassTimeoutDefaultAndExplicitZero(t *testing.T) {
	tests := []struct {
		name string
		sync string
		want time.Duration
	}{
		{"omitted uses the default", "", defaultPassTimeout},
		{"explicit value wins", "sync: {timeout: 90s}", 90 * time.Second},
		{"explicit zero disables the limit", "sync: {timeout: 0s}", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
` + tt.sync + `
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Sync.PassTimeout(); got != tt.want {
				t.Errorf("PassTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPassTimeoutShorterThanIntervalWarns(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
sync: {interval: 10m, timeout: 1m}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "sync.timeout") {
		t.Errorf("warnings = %v, want one about sync.timeout", cfg.Warnings)
	}
}
