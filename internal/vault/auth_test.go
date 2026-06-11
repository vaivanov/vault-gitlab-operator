package vault

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scentbird/vault-gitlab-operator/internal/config"
)

func TestLoginAppRole(t *testing.T) {
	fake := newFakeVault("issued-token")
	fake.approleRoleID = "role-1"
	fake.approleSecretID = "secret-1"
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	t.Setenv("VGO_ROLE_ID", "role-1")
	t.Setenv("VGO_SECRET_ID", "secret-1")
	c, err := New(config.VaultConfig{
		Address: srv.URL,
		Auth: config.VaultAuth{
			Method: "approle",
			AppRole: config.AppRoleAuth{
				Mount:       "approle",
				RoleIDEnv:   "VGO_ROLE_ID",
				SecretIDEnv: "VGO_SECRET_ID",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(t.Context()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The issued token must be usable for reads.
	if _, _, err := c.Read(t.Context(), config.VaultRef{Mount: "kv", Path: "ci/app"}); err != nil {
		t.Fatalf("Read after approle login: %v", err)
	}
	if c.getLoginSecret() == nil {
		t.Error("login secret not stored for renewal")
	}
}

func TestLoginAppRoleBadCredentials(t *testing.T) {
	fake := newFakeVault("issued-token")
	fake.approleRoleID = "role-1"
	fake.approleSecretID = "secret-1"
	srv := fake.start()
	defer srv.Close()

	t.Setenv("VGO_ROLE_ID", "role-1")
	t.Setenv("VGO_SECRET_ID", "wrong")
	c, err := New(config.VaultConfig{
		Address: srv.URL,
		Auth: config.VaultAuth{
			Method: "approle",
			AppRole: config.AppRoleAuth{
				Mount:       "approle",
				RoleIDEnv:   "VGO_ROLE_ID",
				SecretIDEnv: "VGO_SECRET_ID",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Login(t.Context())
	if err == nil || !strings.Contains(err.Error(), "approle login") {
		t.Fatalf("expected approle login error, got %v", err)
	}
}

func TestLoginKubernetes(t *testing.T) {
	fake := newFakeVault("issued-token")
	fake.k8sRole = "vgo"
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	jwtPath := filepath.Join(t.TempDir(), "token")
	if err := writeTestFile(jwtPath, "header.payload.signature"); err != nil {
		t.Fatal(err)
	}

	c, err := New(config.VaultConfig{
		Address: srv.URL,
		Auth: config.VaultAuth{
			Method: "kubernetes",
			Kubernetes: config.KubernetesAuth{
				Mount:   "kubernetes",
				Role:    "vgo",
				JWTFile: jwtPath,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(t.Context()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, _, err := c.Read(t.Context(), config.VaultRef{Mount: "kv", Path: "ci/app"}); err != nil {
		t.Fatalf("Read after kubernetes login: %v", err)
	}
}

func TestKeepAliveStaticTokenReturnsImmediately(t *testing.T) {
	c, err := New(config.VaultConfig{
		Address: "https://vault.example.com",
		Auth:    config.VaultAuth{Method: "token", Token: config.TokenAuth{Env: "X"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		c.KeepAlive(t.Context(), discardLogger())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepAlive did not return for static token method")
	}
}

func TestKeepAliveRelogins(t *testing.T) {
	fake := newFakeVault("issued-token")
	fake.approleRoleID = "r"
	fake.approleSecretID = "s"
	srv := fake.start()
	defer srv.Close()

	t.Setenv("VGO_ROLE_ID", "r")
	t.Setenv("VGO_SECRET_ID", "s")
	c, err := New(config.VaultConfig{
		Address: srv.URL,
		Auth: config.VaultAuth{
			Method: "approle",
			AppRole: config.AppRoleAuth{
				Mount:       "approle",
				RoleIDEnv:   "VGO_ROLE_ID",
				SecretIDEnv: "VGO_SECRET_ID",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(t.Context()); err != nil {
		t.Fatal(err)
	}

	// The fake issues non-renewable tokens, so KeepAlive waits ~2/3 of
	// the lease and re-logins. Shrink the lease to make that immediate.
	secret := c.getLoginSecret()
	secret.Auth.LeaseDuration = 1 // -> ~666ms wait
	c.setLoginSecret(secret)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	go c.KeepAlive(ctx, discardLogger())

	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		calls := fake.loginCalls
		fake.mu.Unlock()
		if calls >= 2 { // initial login + at least one re-login
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("KeepAlive never re-logged in")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
