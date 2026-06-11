package config

import (
	"strings"
	"testing"
)

func TestExpandDuplicateInlineIsError(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: p, field: f}}
        - {key: K, vault: {path: q, field: g}}
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate variable") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestExpandSameKeyDifferentScopeOK(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: p, field: f}, environment_scope: staging}
        - {key: K, vault: {path: q, field: g}, environment_scope: production}
`))
	if err != nil {
		t.Fatalf("same key in different scopes must be valid: %v", err)
	}
	if len(cfg.Expanded[0].Variables) != 2 {
		t.Fatalf("variables = %d, want 2", len(cfg.Expanded[0].Variables))
	}
}

func TestExpandBundleCollisionIsError(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
bundles:
  one:
    - {key: K, vault: {path: p, field: f}}
  two:
    - {key: K, vault: {path: q, field: g}}
targets:
  projects:
    - project: a/b
      bundles: [one, two]
`))
	if err == nil || !strings.Contains(err.Error(), `duplicate variable "K"`) {
		t.Fatalf("expected bundle collision error, got %v", err)
	}
}

func TestExpandInlineOverridesBundle(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
bundles:
  common:
    - {key: K, vault: {path: bundle-path, field: f}, masked: true}
targets:
  projects:
    - project: a/b
      bundles: [common]
      variables:
        - {key: K, vault: {path: inline-path, field: f}}
`))
	if err != nil {
		t.Fatalf("inline override must be valid: %v", err)
	}
	vars := cfg.Expanded[0].Variables
	if len(vars) != 1 {
		t.Fatalf("variables = %d, want 1 (override in place)", len(vars))
	}
	if vars[0].Vault.Path != "inline-path" {
		t.Errorf("override lost: path = %q", vars[0].Vault.Path)
	}
	if vars[0].Masked {
		t.Error("override must fully replace the bundle spec, masked leaked through")
	}
}

func TestExpandInlineDuplicateAfterOverrideIsError(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
bundles:
  common:
    - {key: K, vault: {path: p, field: f}}
targets:
  projects:
    - project: a/b
      bundles: [common]
      variables:
        - {key: K, vault: {path: q, field: f}}
        - {key: K, vault: {path: r, field: f}}
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error for second inline override, got %v", err)
	}
}

func TestExpandBundleScopeOnInstanceIsError(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
bundles:
  scoped:
    - {key: K, vault: {path: p, field: f}, environment_scope: prod}
targets:
  instance:
    bundles: [scoped]
`))
	if err == nil || !strings.Contains(err.Error(), "not supported on instance") {
		t.Fatalf("expected instance-scope error via bundle, got %v", err)
	}
}

func TestExpandBundleScopeOnGroupWarns(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
bundles:
  scoped:
    - {key: K, vault: {path: p, field: f}, environment_scope: prod}
targets:
  groups:
    - group: platform
      bundles: [scoped]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "Premium") && strings.Contains(w, "scoped") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Premium warning for bundle scope on group, warnings = %v", cfg.Warnings)
	}
}

func TestExpandDuplicateFromSecretIsError(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  projects:
    - project: a/b
      variables:
        - {from_secret: {path: p}, prefix: APP_}
        - {from_secret: {path: p}, prefix: APP_}
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate from_secret") {
		t.Fatalf("expected duplicate from_secret error, got %v", err)
	}
}

func TestExpandSameFromSecretDifferentPrefixOK(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
targets:
  projects:
    - project: a/b
      variables:
        - {from_secret: {path: p}, prefix: APP_}
        - {from_secret: {path: p}, prefix: WEB_}
`))
	if err != nil {
		t.Fatalf("distinct prefixes must be valid: %v", err)
	}
	if len(cfg.Expanded[0].Variables) != 2 {
		t.Fatalf("variables = %d, want 2", len(cfg.Expanded[0].Variables))
	}
}

func TestExpandDefaultsOverriddenPerSpec(t *testing.T) {
	cfg, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
defaults:
  protected: true
  masked: true
  raw: false
  variable_type: file
  environment_scope: staging
  description: "custom default"
targets:
  projects:
    - project: a/b
      variables:
        - {key: A, vault: {path: p, field: f}}
        - {key: B, vault: {path: p, field: f}, protected: false, masked: false, raw: true, variable_type: env_var, environment_scope: "*", description: "own"}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vars := cfg.Expanded[0].Variables
	a, b := vars[0], vars[1]
	if !a.Protected || !a.Masked || a.Raw || a.Type != "file" || a.EnvironmentScope != "staging" || a.Description != "custom default" {
		t.Errorf("defaults not applied to A: %+v", a)
	}
	if b.Protected || b.Masked || !b.Raw || b.Type != "env_var" || b.EnvironmentScope != "*" || b.Description != "own" {
		t.Errorf("per-spec overrides not applied to B: %+v", b)
	}
}
