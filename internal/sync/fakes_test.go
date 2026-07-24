package sync

import (
	"context"
	"fmt"
	stdsync "sync"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	"github.com/vaivanov/vault-gitlab-operator/internal/gitlab"
)

// fakeSecrets is an in-memory vault.SecretSource.
type fakeSecrets struct {
	data     map[string]map[string]string // SecretKey() -> fields
	errs     map[string]error             // SecretKey() -> read/version error
	versions map[string]int
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{
		data:     map[string]map[string]string{},
		errs:     map[string]error{},
		versions: map[string]int{},
	}
}

func (f *fakeSecrets) put(key string, fields map[string]string) {
	f.data[key] = fields
	f.versions[key]++
}

func (f *fakeSecrets) Version(_ context.Context, ref config.VaultRef) (int, time.Time, error) {
	if err := f.errs[ref.SecretKey()]; err != nil {
		return 0, time.Time{}, err
	}
	return f.versions[ref.SecretKey()], time.Time{}, nil
}

func (f *fakeSecrets) Read(_ context.Context, ref config.VaultRef) (map[string]string, int, error) {
	if err := f.errs[ref.SecretKey()]; err != nil {
		return nil, 0, err
	}
	data, ok := f.data[ref.SecretKey()]
	if !ok {
		return nil, 0, fmt.Errorf("secret %s: not found", ref.SecretKey())
	}
	return data, f.versions[ref.SecretKey()], nil
}

// fakeStore is an in-memory gitlab.VariableStore. It has no delete
// operation at all — mirroring the VariableStore interface — so the
// no-delete guarantee holds by construction; tests additionally assert
// that pre-seeded variables survive untouched.
type fakeStore struct {
	mu stdsync.Mutex

	vars       map[string][]gitlab.Variable // TargetRef.String() -> variables
	resolveErr map[string]error
	listErr    map[string]error
	createErr  map[string]error // variable key -> error
	updateErr  map[string]error
	ids        map[string]int64 // TargetRef.String() -> resolved numeric ID
	nextID     int64

	creates, updates, resolves int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		vars:       map[string][]gitlab.Variable{},
		resolveErr: map[string]error{},
		listErr:    map[string]error{},
		createErr:  map[string]error{},
		updateErr:  map[string]error{},
		ids:        map[string]int64{},
	}
}

// alias pins the numeric ID a target ref resolves to, so a test can make
// two refs (a path and an ID, say) denote the same GitLab object.
func (f *fakeStore) alias(target string, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids[target] = id
}

func (f *fakeStore) seed(target string, v gitlab.Variable) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vars[target] = append(f.vars[target], v)
}

func (f *fakeStore) get(target, key, scope string) *gitlab.Variable {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.vars[target] {
		if v.Key == key && v.EnvironmentScope == scope {
			out := v
			return &out
		}
	}
	return nil
}

func (f *fakeStore) ResolveTarget(_ context.Context, t *config.TargetRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	if err := f.resolveErr[t.String()]; err != nil {
		return err
	}
	if t.Kind == config.KindInstance {
		return nil
	}
	// Distinct, stable IDs per ref, as real GitLab hands out — unless the
	// test aliased two refs onto one ID.
	id, ok := f.ids[t.String()]
	if !ok {
		f.nextID++
		id = f.nextID
		f.ids[t.String()] = id
	}
	t.ID = id
	return nil
}

func (f *fakeStore) List(_ context.Context, t config.TargetRef) ([]gitlab.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listErr[t.String()]; err != nil {
		return nil, err
	}
	return append([]gitlab.Variable(nil), f.vars[t.String()]...), nil
}

func (f *fakeStore) Create(_ context.Context, t config.TargetRef, v gitlab.Variable) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if err := f.createErr[v.Key]; err != nil {
		return err
	}
	f.vars[t.String()] = append(f.vars[t.String()], v)
	return nil
}

func (f *fakeStore) Update(_ context.Context, t config.TargetRef, v gitlab.Variable) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	if err := f.updateErr[v.Key]; err != nil {
		return err
	}
	for i, existing := range f.vars[t.String()] {
		if existing.Key == v.Key && existing.EnvironmentScope == v.EnvironmentScope {
			f.vars[t.String()][i] = v
			return nil
		}
	}
	return fmt.Errorf("update %s: not found", v.Key)
}

func (f *fakeStore) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates + f.updates
}
