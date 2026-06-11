package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// e2eFixture wires minimal fake Vault and GitLab servers plus a config
// file referencing them, to exercise once/diff through the real CLI.
type e2eFixture struct {
	configPath string
	mu         sync.Mutex
	vars       []map[string]any // project 1 variables
	writes     int
}

func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	f := &e2eFixture{}

	vaultSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "vault-tok" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":["permission denied"]}`)
			return
		}
		switch r.URL.Path {
		case "/v1/secret/metadata/ci/app":
			fmt.Fprint(w, `{"data":{"current_version":1,"updated_time":"2026-06-01T12:00:00Z"}}`)
		case "/v1/secret/data/ci/app":
			fmt.Fprint(w, `{"data":{"data":{"password":"hunter22hunter22"},"metadata":{"version":1}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[]}`)
		}
	}))
	t.Cleanup(vaultSrv.Close)

	gitlabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "gitlab-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.EscapedPath()
		switch {
		case r.Method == http.MethodGet && path == "/api/v4/projects/a%2Fb":
			fmt.Fprint(w, `{"id":1}`)
		case r.Method == http.MethodGet && path == "/api/v4/projects/1/variables":
			w.Header().Set("X-Next-Page", "")
			_ = json.NewEncoder(w).Encode(f.vars)
		case r.Method == http.MethodPost && path == "/api/v4/projects/1/variables":
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v["environment_scope"] == nil {
				v["environment_scope"] = "*"
			}
			f.vars = append(f.vars, v)
			f.writes++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(v)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"message":"404 for %s %s"}`, r.Method, path)
		}
	}))
	t.Cleanup(gitlabSrv.Close)

	t.Setenv("VAULT_TOKEN", "vault-tok")
	t.Setenv("GITLAB_TOKEN", "gitlab-tok")

	f.configPath = filepath.Join(t.TempDir(), "config.yaml")
	cfg := fmt.Sprintf(`
vault:
  address: %s
gitlab:
  url: %s
  token_env: GITLAB_TOKEN
targets:
  projects:
    - project: a/b
      variables:
        - {key: DB_PASSWORD, vault: {path: ci/app, field: password}, masked: true}
`, vaultSrv.URL, gitlabSrv.URL)
	if err := writeFile(f.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOnceAndDiffEndToEnd(t *testing.T) {
	f := newE2EFixture(t)

	// 1. diff before sync: change pending -> exit 3 with the gating flag.
	if got := run([]string{"diff", "--exit-nonzero-on-diff", "-c", f.configPath}); got != exitDiffPending {
		t.Fatalf("diff before sync = %d, want %d", got, exitDiffPending)
	}
	if f.writes != 0 {
		t.Fatalf("diff performed %d writes", f.writes)
	}

	// 2. once: creates the variable.
	if got := run([]string{"once", "-c", f.configPath}); got != exitOK {
		t.Fatalf("once = %d, want %d", got, exitOK)
	}
	if f.writes != 1 || len(f.vars) != 1 {
		t.Fatalf("after once: writes=%d vars=%d", f.writes, len(f.vars))
	}
	if f.vars[0]["key"] != "DB_PASSWORD" || f.vars[0]["masked"] != true {
		t.Errorf("created variable wrong: %+v", f.vars[0])
	}

	// 3. once again: idempotent, no extra writes.
	if got := run([]string{"once", "-c", f.configPath}); got != exitOK {
		t.Fatalf("second once = %d, want %d", got, exitOK)
	}
	if f.writes != 1 {
		t.Fatalf("second once performed extra writes: %d", f.writes)
	}

	// 4. diff after sync: converged -> exit 0 even with the gating flag.
	if got := run([]string{"diff", "--exit-nonzero-on-diff", "-c", f.configPath}); got != exitOK {
		t.Fatalf("diff after sync = %d, want %d", got, exitOK)
	}
}

func TestOnceExitCodeOnSyncErrors(t *testing.T) {
	f := newE2EFixture(t)
	// Break the Vault token: reads fail, sync reports errors.
	t.Setenv("VAULT_TOKEN", "wrong-token")

	if got := run([]string{"once", "-c", f.configPath}); got != exitSyncErrors {
		t.Fatalf("once with broken vault = %d, want %d", got, exitSyncErrors)
	}
}

func TestOnceConfigErrorExitCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := writeFile(path, "targets: {}\n"); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"once", "-c", path}); got != exitConfigError {
		t.Fatalf("once with bad config = %d, want %d", got, exitConfigError)
	}
}

func TestDiffWithoutGatingFlagExitsZero(t *testing.T) {
	f := newE2EFixture(t)
	if got := run([]string{"diff", "-c", f.configPath}); got != exitOK {
		t.Fatalf("diff = %d, want %d", got, exitOK)
	}
}
