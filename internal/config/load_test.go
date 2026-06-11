package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestLoadValidMinimal(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "valid_minimal.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Built-in defaults.
	if got := cfg.Sync.Interval.Std(); got != 5*time.Minute {
		t.Errorf("default interval = %v, want 5m", got)
	}
	if cfg.Sync.Concurrency != 4 {
		t.Errorf("default concurrency = %d, want 4", cfg.Sync.Concurrency)
	}
	if cfg.Sync.OnMaskedViolation != MaskedViolationError {
		t.Errorf("default on_masked_violation = %q", cfg.Sync.OnMaskedViolation)
	}
	if cfg.Vault.Auth.Method != "token" {
		t.Errorf("default auth method = %q, want token", cfg.Vault.Auth.Method)
	}
	if cfg.Vault.Auth.Token.Env != "VAULT_TOKEN" {
		t.Errorf("default token env = %q, want VAULT_TOKEN", cfg.Vault.Auth.Token.Env)
	}

	if len(cfg.Expanded) != 1 {
		t.Fatalf("expanded targets = %d, want 1", len(cfg.Expanded))
	}
	target := cfg.Expanded[0]
	if target.Ref.Kind != KindProject || target.Ref.Ref != "group/app" {
		t.Errorf("target ref = %+v", target.Ref)
	}
	if len(target.Variables) != 1 {
		t.Fatalf("variables = %d, want 1", len(target.Variables))
	}
	v := target.Variables[0]
	if v.Key != "API_KEY" {
		t.Errorf("key = %q", v.Key)
	}
	// Defaults applied to the spec.
	if v.Vault.Mount != "secret" {
		t.Errorf("default mount = %q, want secret", v.Vault.Mount)
	}
	if v.Type != "env_var" || v.Protected || v.Masked || !v.Raw {
		t.Errorf("defaults not applied: %+v", v)
	}
	if v.EnvironmentScope != "*" {
		t.Errorf("default scope = %q, want *", v.EnvironmentScope)
	}
	if v.Description == "" {
		t.Error("default description empty")
	}
}

func TestLoadValidFull(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "valid_full.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Sync.Interval.Std(); got != 10*time.Minute {
		t.Errorf("interval = %v, want 10m", got)
	}
	if got := cfg.Sync.Jitter.Std(); got != 30*time.Second {
		t.Errorf("jitter = %v, want 30s", got)
	}
	if cfg.Sync.OnMaskedViolation != MaskedViolationSkipWarn {
		t.Errorf("on_masked_violation = %q", cfg.Sync.OnMaskedViolation)
	}
	if cfg.Vault.Auth.Method != "approle" {
		t.Errorf("auth method = %q", cfg.Vault.Auth.Method)
	}
	if cfg.Vault.Auth.AppRole.Mount != "approle" {
		t.Errorf("approle mount default = %q", cfg.Vault.Auth.AppRole.Mount)
	}

	// instance + 1 group + 2 projects
	if len(cfg.Expanded) != 4 {
		t.Fatalf("expanded targets = %d, want 4", len(cfg.Expanded))
	}

	byRef := map[string]Target{}
	for _, tg := range cfg.Expanded {
		byRef[tg.Ref.String()] = tg
	}

	inst := byRef["instance"]
	if len(inst.Variables) != 1 || inst.Variables[0].Key != "GLOBAL_REGISTRY_PASSWORD" {
		t.Errorf("instance variables = %+v", inst.Variables)
	}

	group := byRef["group:platform"]
	// bundle backend-common (2) + 1 inline
	if len(group.Variables) != 3 {
		t.Fatalf("group variables = %d, want 3", len(group.Variables))
	}
	if group.Variables[2].Vault.Mount != "kv-infra" {
		t.Errorf("explicit mount not preserved: %+v", group.Variables[2].Vault)
	}
	// Bundle spec inherits the configured default mount "kv".
	if group.Variables[0].Vault.Mount != "kv" {
		t.Errorf("bundle mount = %q, want kv", group.Variables[0].Vault.Mount)
	}

	backend := byRef["project:platform/backend"]
	// backend-common (2) + deploy-keys (2) + DB_PASSWORD + from_secret = 6,
	// NPM_TOKEN inline overrides the bundle entry in place.
	if len(backend.Variables) != 6 {
		t.Fatalf("backend variables = %d, want 6: %+v", len(backend.Variables), backend.Variables)
	}
	var npm *ResolvedVariable
	for i := range backend.Variables {
		if backend.Variables[i].Key == "NPM_TOKEN" {
			if npm != nil {
				t.Fatal("NPM_TOKEN present twice after override")
			}
			npm = &backend.Variables[i]
		}
	}
	if npm == nil {
		t.Fatal("NPM_TOKEN missing")
	}
	if npm.Vault.Path != "ci/backend/npm-override" {
		t.Errorf("inline override lost: NPM_TOKEN path = %q", npm.Vault.Path)
	}

	var fromSecret *ResolvedVariable
	for i := range backend.Variables {
		if backend.Variables[i].FromSecret != nil {
			fromSecret = &backend.Variables[i]
		}
	}
	if fromSecret == nil {
		t.Fatal("from_secret spec missing")
	}
	if fromSecret.Prefix != "APP_" || fromSecret.FromSecret.Path != "ci/backend/dotenv" {
		t.Errorf("from_secret = %+v", fromSecret)
	}

	numeric := byRef["project:1234"]
	if len(numeric.Variables) != 2 {
		t.Errorf("numeric-ID project variables = %d, want 2", len(numeric.Variables))
	}
}

func TestLoadFileMissing(t *testing.T) {
	if _, err := Load(filepath.Join("testdata", "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
tarrgets: {}
`))
	if err == nil || !strings.Contains(err.Error(), "tarrgets") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestParseRejectsBadDuration(t *testing.T) {
	_, err := Parse([]byte(`
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
sync: {interval: "5 minutes"}
targets: {projects: [{project: a/b, variables: [{key: K, vault: {path: p, field: f}}]}]}
`))
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("expected duration error, got %v", err)
	}
}

func TestResolveSecret(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		t.Setenv("VGO_TEST_TOKEN", "tok-123")
		g := GitLabConfig{TokenEnv: "VGO_TEST_TOKEN"}
		got, err := g.Token()
		if err != nil || got.Reveal() != "tok-123" {
			t.Fatalf("Token() = %v, %v", got.Reveal(), err)
		}
	})
	t.Run("env unset", func(t *testing.T) {
		g := GitLabConfig{TokenEnv: "VGO_TEST_TOKEN_UNSET"}
		if _, err := g.Token(); err == nil {
			t.Fatal("expected error for unset env var")
		}
	})
	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "token")
		if err := writeFile(path, "tok-456\n"); err != nil {
			t.Fatal(err)
		}
		g := GitLabConfig{TokenFile: path}
		got, err := g.Token()
		if err != nil || got.Reveal() != "tok-456" {
			t.Fatalf("Token() = %q, %v (want whitespace trimmed)", got.Reveal(), err)
		}
	})
	t.Run("file missing", func(t *testing.T) {
		g := GitLabConfig{TokenFile: filepath.Join(t.TempDir(), "nope")}
		if _, err := g.Token(); err == nil {
			t.Fatal("expected error for missing file")
		}
	})
	t.Run("approle", func(t *testing.T) {
		t.Setenv("VGO_TEST_ROLE_ID", "role-1")
		path := filepath.Join(t.TempDir(), "secret-id")
		if err := writeFile(path, "sec-1"); err != nil {
			t.Fatal(err)
		}
		a := AppRoleAuth{RoleIDEnv: "VGO_TEST_ROLE_ID", SecretIDFile: path}
		role, secret, err := a.Credentials()
		if err != nil || role != "role-1" || secret.Reveal() != "sec-1" {
			t.Fatalf("Credentials() = %q, %q, %v", role, secret.Reveal(), err)
		}
	})
}
