package vault

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

func TestCachedReadSkipsDataWhenVersionUnchanged(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"password": "first"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}

	for i := 0; i < 3; i++ {
		data, version, err := cached.Read(t.Context(), ref)
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if version != 1 || data["password"] != "first" {
			t.Fatalf("Read #%d = %v v%d", i, data, version)
		}
	}

	metadata, data := fake.hits("kv/ci/app")
	if data != 1 {
		t.Errorf("data endpoint hit %d times across 3 reads, want exactly 1", data)
	}
	if metadata != 3 {
		t.Errorf("metadata endpoint hit %d times, want 3 (once per Read)", metadata)
	}
}

func TestCachedReadRefetchesAfterVersionBump(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"password": "first"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}

	if _, _, err := cached.Read(t.Context(), ref); err != nil {
		t.Fatal(err)
	}

	fake.put("kv/ci/app", 2, map[string]any{"password": "second"})

	data, version, err := cached.Read(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 || data["password"] != "second" {
		t.Fatalf("after bump: %v v%d, want second/v2", data, version)
	}

	if _, dataHits := fake.hits("kv/ci/app"); dataHits != 2 {
		t.Errorf("data endpoint hit %d times, want 2", dataHits)
	}
}

func TestCachedReadPropagatesVersionError(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	_, _, err := cached.Read(t.Context(), config.VaultRef{Mount: "kv", Path: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	// The data endpoint must not have been touched at all.
	if _, dataHits := fake.hits("kv/missing"); dataHits != 0 {
		t.Errorf("data endpoint hit %d times for a missing secret", dataHits)
	}
}

func TestCachedForget(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}

	if _, _, err := cached.Read(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	cached.Forget(ref)
	if _, _, err := cached.Read(t.Context(), ref); err != nil {
		t.Fatal(err)
	}

	if _, dataHits := fake.hits("kv/ci/app"); dataHits != 2 {
		t.Errorf("data endpoint hit %d times, want 2 after Forget", dataHits)
	}
}

func TestCachedSharedAcrossFields(t *testing.T) {
	// Two refs into the same secret (different fields) share one cache
	// entry: SecretKey ignores the field.
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"user": "u", "pass": "p"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	refUser := config.VaultRef{Mount: "kv", Path: "ci/app", Field: "user"}
	refPass := config.VaultRef{Mount: "kv", Path: "ci/app", Field: "pass"}

	if _, _, err := cached.Read(t.Context(), refUser); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cached.Read(t.Context(), refPass); err != nil {
		t.Fatal(err)
	}

	if _, dataHits := fake.hits("kv/ci/app"); dataHits != 1 {
		t.Errorf("data endpoint hit %d times, want 1 (shared across fields)", dataHits)
	}
}

func TestCachedVersionMemoizedWithinPass(t *testing.T) {
	// A secret referenced by many variables across many targets must cost
	// one metadata call per pass, not one per reference.
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"user": "u", "pass": "p"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	refs := []config.VaultRef{
		{Mount: "kv", Path: "ci/app", Field: "user"},
		{Mount: "kv", Path: "ci/app", Field: "pass"},
		{Mount: "kv", Path: "ci/app"},
	}

	cached.BeginPass()
	for i, ref := range refs {
		if _, _, err := cached.Read(t.Context(), ref); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	metadata, data := fake.hits("kv/ci/app")
	if metadata != 1 || data != 1 {
		t.Errorf("pass 1: metadata=%d data=%d, want 1 and 1", metadata, data)
	}

	// A new pass must re-check the version, but not re-read unchanged data.
	cached.BeginPass()
	for _, ref := range refs {
		if _, _, err := cached.Read(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
	}
	metadata, data = fake.hits("kv/ci/app")
	if metadata != 2 {
		t.Errorf("pass 2: metadata=%d, want 2 (one re-check per pass)", metadata)
	}
	if data != 1 {
		t.Errorf("pass 2: data=%d, want 1 (version unchanged)", data)
	}

	// ... and it must still notice a bump.
	fake.put("kv/ci/app", 2, map[string]any{"user": "u2", "pass": "p2"})
	cached.BeginPass()
	got, version, err := cached.Read(t.Context(), refs[0])
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 || got["user"] != "u2" {
		t.Errorf("pass 3 = %v v%d, want u2/v2", got, version)
	}
}

func TestCachedVersionCheckedEveryReadWithoutPasses(t *testing.T) {
	// Without BeginPass there is no pass window, so every Read verifies.
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}
	for range 3 {
		if _, _, err := cached.Read(t.Context(), ref); err != nil {
			t.Fatal(err)
		}
	}
	if metadata, _ := fake.hits("kv/ci/app"); metadata != 3 {
		t.Errorf("metadata=%d, want 3", metadata)
	}
}

func TestCachedReadReturnsACopy(t *testing.T) {
	// Callers get their own map; mutating it must not poison the cache
	// shared by every concurrently reconciling target.
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}

	first, _, err := cached.Read(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	first["k"] = "tampered"
	delete(first, "k")

	second, _, err := cached.Read(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if second["k"] != "v" {
		t.Errorf("cache poisoned by caller mutation: %v", second)
	}
}

func TestCachedConcurrentReadsShareOneMetadataCall(t *testing.T) {
	fake := newFakeVault("tok")
	srv := fake.start()
	defer srv.Close()
	fake.put("kv/ci/app", 1, map[string]any{"k": "v"})

	cached := NewCached(newTestClient(t, srv.URL, "tok"))
	ref := config.VaultRef{Mount: "kv", Path: "ci/app"}

	cached.BeginPass()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := cached.Read(context.Background(), ref); err != nil {
				t.Errorf("concurrent read: %v", err)
			}
		}()
	}
	wg.Wait()

	metadata, data := fake.hits("kv/ci/app")
	if metadata != 1 || data != 1 {
		t.Errorf("metadata=%d data=%d across 8 concurrent reads, want 1 and 1", metadata, data)
	}
}
