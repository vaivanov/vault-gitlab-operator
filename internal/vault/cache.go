package vault

import (
	"context"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

// CachedSource wraps a SecretSource with a version-keyed in-memory cache:
// Read performs a cheap metadata Version call and only fetches the secret
// data when the version differs from the cached one. The cache keeps full
// data because GitLab drift detection needs values on every run even when
// Vault has not changed.
//
// Version lookups are additionally memoized per reconcile pass: a secret
// referenced by twenty variables across fifty targets is checked once,
// not a thousand times. BeginPass opens a new pass; until it is called
// every Read re-checks the version.
type CachedSource struct {
	src SecretSource

	// pass is the current pass number; 0 means no pass has been declared
	// and version lookups are never reused.
	pass atomic.Uint64

	mu      sync.Mutex
	entries map[string]*cacheEntry // keyed by VaultRef.SecretKey()
}

type cacheEntry struct {
	// mu serializes refreshes of this one secret so concurrent targets
	// share a single metadata call instead of racing to duplicate it.
	mu sync.Mutex

	version int
	data    map[string]string
	pass    uint64 // pass in which the version was last verified
}

// NewCached wraps src with the version-keyed cache.
func NewCached(src SecretSource) *CachedSource {
	return &CachedSource{src: src, entries: map[string]*cacheEntry{}}
}

// BeginPass starts a new reconcile pass, invalidating the memoized
// version lookups of the previous one. Cached secret data survives; only
// the "already checked" marks are dropped.
func (c *CachedSource) BeginPass() { c.pass.Add(1) }

// Version delegates to the underlying source (always a live call).
func (c *CachedSource) Version(ctx context.Context, ref config.VaultRef) (int, time.Time, error) {
	return c.src.Version(ctx, ref)
}

// Read returns cached data when the current Vault version matches the
// cached one, fetching from the source otherwise. The returned map is a
// copy: callers must not be able to mutate the shared cache.
func (c *CachedSource) Read(ctx context.Context, ref config.VaultRef) (map[string]string, int, error) {
	entry := c.entryFor(ref.SecretKey())
	pass := c.pass.Load()

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Already verified in this pass: the data cannot have gone stale
	// since, because a pass is exactly the window we promise freshness for.
	if entry.data != nil && pass != 0 && entry.pass == pass {
		return maps.Clone(entry.data), entry.version, nil
	}

	version, _, err := c.src.Version(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	if entry.data != nil && entry.version == version {
		entry.pass = pass
		return maps.Clone(entry.data), entry.version, nil
	}

	data, readVersion, err := c.src.Read(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	entry.version, entry.data, entry.pass = readVersion, data, pass
	return maps.Clone(data), readVersion, nil
}

// Forget drops the cached entry for a secret (used in tests and reload).
func (c *CachedSource) Forget(ref config.VaultRef) {
	c.mu.Lock()
	delete(c.entries, ref.SecretKey())
	c.mu.Unlock()
}

func (c *CachedSource) entryFor(key string) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		entry = &cacheEntry{}
		c.entries[key] = entry
	}
	return entry
}
