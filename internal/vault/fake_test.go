package vault

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// fakeVault is an httptest-backed KV v2 server covering the two endpoints
// the client uses: GET /v1/:mount/metadata/:path and /v1/:mount/data/:path.
// It enforces the X-Vault-Token header and counts hits per endpoint so
// tests can assert cache behaviour.
type fakeVault struct {
	token string

	mu           sync.Mutex
	secrets      map[string]*fakeSecret // keyed by mount/path
	metadataHits map[string]int
	dataHits     map[string]int
	sealed       bool
}

type fakeSecret struct {
	version int
	data    map[string]any
	updated time.Time
	deleted bool // current version deleted -> data: null
}

func newFakeVault(token string) *fakeVault {
	return &fakeVault{
		token:        token,
		secrets:      map[string]*fakeSecret{},
		metadataHits: map[string]int{},
		dataHits:     map[string]int{},
	}
}

func (f *fakeVault) put(key string, version int, data map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[key] = &fakeSecret{
		version: version,
		data:    data,
		updated: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(version) * time.Hour),
	}
}

func (f *fakeVault) hits(key string) (metadata, data int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metadataHits[key], f.dataHits[key]
}

func (f *fakeVault) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		if f.sealed {
			writeErrors(w, http.StatusServiceUnavailable, "Vault is sealed")
			return
		}
		if r.Header.Get("X-Vault-Token") != f.token {
			writeErrors(w, http.StatusForbidden, "permission denied")
			return
		}

		// /v1/<mount>/<op>/<path...>
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/"), "/", 3)
		if len(parts) != 3 {
			writeErrors(w, http.StatusNotFound, "unsupported path")
			return
		}
		mount, op, path := parts[0], parts[1], parts[2]
		key := mount + "/" + path
		secret, exists := f.secrets[key]

		switch op {
		case "metadata":
			f.metadataHits[key]++
			if !exists {
				writeErrors(w, http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{
				"data": map[string]any{
					"current_version": secret.version,
					"updated_time":    secret.updated.Format(time.RFC3339),
					"created_time":    secret.updated.Add(-time.Hour).Format(time.RFC3339),
					"oldest_version":  1,
				},
			})
		case "data":
			f.dataHits[key]++
			if !exists {
				writeErrors(w, http.StatusNotFound)
				return
			}
			var data any = secret.data
			if secret.deleted {
				data = nil
			}
			writeJSON(w, map[string]any{
				"data": map[string]any{
					"data": data,
					"metadata": map[string]any{
						"version":      secret.version,
						"created_time": secret.updated.Format(time.RFC3339),
					},
				},
			})
		default:
			writeErrors(w, http.StatusNotFound, fmt.Sprintf("unsupported op %q", op))
		}
	})
}

func (f *fakeVault) start() *httptest.Server {
	return httptest.NewServer(f.handler())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrors(w http.ResponseWriter, code int, msgs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if msgs == nil {
		msgs = []string{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": msgs})
}
