package vault

import (
	"context"
	"sync"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

// CachedSource wraps a SecretSource with a version-keyed in-memory cache:
// Read first performs a cheap metadata Version call and only fetches the
// secret data when the version differs from the cached one. The cache
// keeps full data because GitLab drift detection needs values on every
// run even when Vault has not changed.
type CachedSource struct {
	src SecretSource

	mu      sync.Mutex
	entries map[string]cacheEntry // keyed by VaultRef.SecretKey()
}

type cacheEntry struct {
	version int
	data    map[string]string
}

// NewCached wraps src with the version-keyed cache.
func NewCached(src SecretSource) *CachedSource {
	return &CachedSource{src: src, entries: map[string]cacheEntry{}}
}

// Version delegates to the underlying source (always a live call).
func (c *CachedSource) Version(ctx context.Context, ref config.VaultRef) (int, time.Time, error) {
	return c.src.Version(ctx, ref)
}

// Read returns cached data when the current Vault version matches the
// cached one, fetching from the source otherwise.
func (c *CachedSource) Read(ctx context.Context, ref config.VaultRef) (map[string]string, int, error) {
	version, _, err := c.src.Version(ctx, ref)
	if err != nil {
		return nil, 0, err
	}

	key := ref.SecretKey()
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && entry.version == version {
		return entry.data, entry.version, nil
	}

	data, readVersion, err := c.src.Read(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	c.mu.Lock()
	c.entries[key] = cacheEntry{version: readVersion, data: data}
	c.mu.Unlock()
	return data, readVersion, nil
}

// Forget drops the cached entry for a secret (used in tests and reload).
func (c *CachedSource) Forget(ref config.VaultRef) {
	c.mu.Lock()
	delete(c.entries, ref.SecretKey())
	c.mu.Unlock()
}
