package vault

import (
	"errors"
	"strings"
	"testing"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

func newTestClient(t *testing.T, addr, token string) *Client {
	t.Helper()
	t.Setenv("VGO_TEST_VAULT_TOKEN", token)
	cfg := config.VaultConfig{
		Address: addr,
		Auth: config.VaultAuth{
			Method: "token",
			Token:  config.TokenAuth{Env: "VGO_TEST_VAULT_TOKEN"},
		},
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVersion(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 3, map[string]any{"k": "v"})

	c := newTestClient(t, srv.URL, "tok")
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}

	version, updated, err := c.Version(t.Context(), ref)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != 3 {
		t.Errorf("version = %d, want 3", version)
	}
	if updated.IsZero() {
		t.Error("updated_time not parsed")
	}

	// Version must never hit the data endpoint.
	if _, data := fake.hits("kv/ci/app"); data != 0 {
		t.Errorf("Version caused %d data reads", data)
	}
}

func TestRead(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 2, map[string]any{
		"password": "hunter22",
		"port":     5432,
		"debug":    true,
		"empty":    nil,
		"nested":   map[string]any{"a": 1},
	})

	c := newTestClient(t, srv.URL, "tok")
	data, version, err := c.Read(t.Context(), config.VaultRef{Mount: "kv", Path: "ci/app"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if version != 2 {
		t.Errorf("version = %d, want 2", version)
	}
	want := map[string]string{
		"password": "hunter22",
		"port":     "5432",
		"debug":    "true",
		"empty":    "",
		"nested":   `{"a":1}`,
	}
	for k, w := range want {
		if data[k] != w {
			t.Errorf("data[%q] = %q, want %q", k, data[k], w)
		}
	}
}

func TestReadNotFound(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok")
	ref := config.VaultRef{Mount: "kv", Path: "missing"}

	if _, _, err := c.Read(t.Context(), ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read missing: err = %v, want ErrNotFound", err)
	}
	if _, _, err := c.Version(t.Context(), ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("Version missing: err = %v, want ErrNotFound", err)
	}
}

func TestReadDeletedVersion(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 4, map[string]any{"k": "v"})
	fake.secrets["kv/ci/app"].deleted = true

	c := newTestClient(t, srv.URL, "tok")
	_, _, err := c.Read(t.Context(), config.VaultRef{Mount: "kv", Path: "ci/app"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted version: err = %v, want ErrNotFound", err)
	}
}

func TestPermissionDenied(t *testing.T) {
	fake := newFakeVault("right-token")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	c := newTestClient(t, srv.URL, "wrong-token")
	_, _, err := c.Read(t.Context(), config.VaultRef{Mount: "kv", Path: "ci/app"})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected permission denied, got %v", err)
	}
}

func TestSealedVault(t *testing.T) {
	fake := newFakeVault("tok")
	fake.sealed = true
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok")
	_, _, err := c.Version(t.Context(), config.VaultRef{Mount: "kv", Path: "ci/app"})
	if err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Errorf("expected sealed error, got %v", err)
	}
}

func TestLoginMissingCredentials(t *testing.T) {
	// approle without credential refs and kubernetes without a JWT file
	// must fail loudly at login time.
	for _, method := range []string{"approle", "kubernetes"} {
		cfg := config.VaultConfig{
			Address: "https://vault.example.com",
			Auth: config.VaultAuth{
				Method:     method,
				Kubernetes: config.KubernetesAuth{Role: "x", JWTFile: "/nonexistent/jwt"},
			},
		}
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Login(t.Context()); err == nil {
			t.Errorf("method %s: expected credential error", method)
		}
	}
}

func TestUpdatedTimeOrdering(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/a", 1, map[string]any{"k": "v"})
	fake.put("kv/b", 5, map[string]any{"k": "v"})

	c := newTestClient(t, srv.URL, "tok")
	_, t1, err := c.Version(t.Context(), config.VaultRef{Mount: "kv", Path: "a"})
	if err != nil {
		t.Fatal(err)
	}
	_, t5, err := c.Version(t.Context(), config.VaultRef{Mount: "kv", Path: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !t5.After(t1) {
		t.Errorf("updated times not ordered: %v vs %v", t1, t5)
	}
}
