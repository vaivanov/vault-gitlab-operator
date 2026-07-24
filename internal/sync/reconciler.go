package sync

import (
	"context"
	"fmt"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	"github.com/vaivanov/vault-gitlab-operator/internal/gitlab"
	"github.com/vaivanov/vault-gitlab-operator/internal/vault"
)

// Reconciler runs one desired-state pass: config + Vault -> GitLab.
type Reconciler struct {
	Config  *config.Config
	Secrets vault.SecretSource
	Store   gitlab.VariableStore
	Log     *slog.Logger
}

// passResetter is implemented by secret sources that memoize Vault
// version lookups for the duration of one pass (*vault.CachedSource).
// Run resets them so every pass re-checks each secret exactly once.
type passResetter interface{ BeginPass() }

// Run reconciles every expanded target. Targets run concurrently (capped
// by sync.concurrency); variables within one target apply sequentially to
// avoid racing GitLab on duplicate-key scopes. Failures are isolated: one
// broken target or Vault path never aborts the others.
func (r *Reconciler) Run(ctx context.Context, dryRun bool) *Report {
	start := time.Now()
	results := make([]TargetResult, len(r.Config.Expanded))

	if p, ok := r.Secrets.(passResetter); ok {
		p.BeginPass()
	}
	claimed := newClaimSet()

	sem := make(chan struct{}, r.Config.Sync.Concurrency)
	var wg stdsync.WaitGroup
	for i, t := range r.Config.Expanded {
		wg.Add(1)
		go func(i int, t config.Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = r.syncTarget(ctx, t, dryRun, claimed)
		}(i, t)
	}
	wg.Wait()

	report := &Report{Targets: results, Duration: time.Since(start), DryRun: dryRun}
	c := report.Counts()
	r.Log.Info("reconcile finished",
		"dry_run", dryRun,
		"created", c.Created,
		"updated", c.Updated,
		"unchanged", c.Unchanged,
		"skipped", c.Skipped,
		"failed", c.Failed,
		"target_errors", c.TargetErrors,
		"duration", report.Duration.Round(time.Millisecond),
	)
	return report
}

func (r *Reconciler) syncTarget(ctx context.Context, t config.Target, dryRun bool, claimed *claimSet) TargetResult {
	log := r.Log.With("target", t.Ref.String())
	res := TargetResult{Target: t.Ref}

	if err := r.Store.ResolveTarget(ctx, &t.Ref); err != nil {
		res.Err = err
		log.Error("target resolution failed", "error", err)
		return res
	}
	res.Target = t.Ref

	// Config validation rejects textually identical targets, but a group
	// or project can also be named by path in one entry and by numeric ID
	// in another. Both would write the same variables concurrently, so the
	// second one to resolve is refused rather than left to race.
	if other, dup := claimed.claim(t.Ref); dup {
		res.Err = fmt.Errorf("resolves to the same GitLab object as target %s; declare it once", other)
		log.Error("duplicate target", "error", res.Err)
		return res
	}

	desired, skips := r.buildDesired(ctx, t)

	observed, err := r.Store.List(ctx, t.Ref)
	if err != nil {
		res.Err = fmt.Errorf("list variables: %w", err)
		log.Error("listing variables failed", "error", err)
		return res
	}

	actions := diffTarget(t.Ref, desired, observed)

	for i := range actions {
		a := &actions[i]
		switch a.Op {
		case OpNoop:
			log.Debug("unchanged", "key", a.Variable.Key, "scope", a.Variable.EnvironmentScope)
			continue
		case OpCreate, OpUpdate:
			if dryRun {
				log.Info("planned", "op", string(a.Op), "key", a.Variable.Key, "scope", a.Variable.EnvironmentScope)
				continue
			}
			if err := r.apply(ctx, *a); err != nil {
				a.Err = err
				log.Error("apply failed", "op", string(a.Op), "key", a.Variable.Key, "error", err)
				continue
			}
			log.Info("applied", "op", string(a.Op), "key", a.Variable.Key, "scope", a.Variable.EnvironmentScope)
		}
	}

	for _, s := range skips {
		if s.Err != nil {
			log.Error("skipped", "key", s.Variable.Key, "reason", s.Reason)
		} else {
			log.Warn("skipped", "key", s.Variable.Key, "reason", s.Reason)
		}
	}

	res.Actions = append(actions, skips...)
	return res
}

// claimSet records which concrete GitLab objects a pass has already
// started reconciling, keyed by kind and resolved numeric ID.
type claimSet struct {
	mu stdsync.Mutex
	by map[string]string // "kind:id" -> the target that claimed it
}

func newClaimSet() *claimSet { return &claimSet{by: map[string]string{}} }

// claim registers ref and reports the target that got there first when
// the object is already claimed.
func (c *claimSet) claim(ref config.TargetRef) (string, bool) {
	key := fmt.Sprintf("%s:%d", ref.Kind, ref.ID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if first, dup := c.by[key]; dup {
		return first, true
	}
	c.by[key] = ref.String()
	return "", false
}

func (r *Reconciler) apply(ctx context.Context, a Action) error {
	switch a.Op {
	case OpCreate:
		return r.Store.Create(ctx, a.Target, a.Variable)
	case OpUpdate:
		return r.Store.Update(ctx, a.Target, a.Variable)
	default:
		return nil
	}
}
