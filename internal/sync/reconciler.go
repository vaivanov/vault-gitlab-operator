package sync

import (
	"context"
	"fmt"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/scentbird/vault-gitlab-operator/internal/config"
	"github.com/scentbird/vault-gitlab-operator/internal/gitlab"
	"github.com/scentbird/vault-gitlab-operator/internal/vault"
)

// Reconciler runs one desired-state pass: config + Vault -> GitLab.
type Reconciler struct {
	Config  *config.Config
	Secrets vault.SecretSource
	Store   gitlab.VariableStore
	Log     *slog.Logger
}

// Run reconciles every expanded target. Targets run concurrently (capped
// by sync.concurrency); variables within one target apply sequentially to
// avoid racing GitLab on duplicate-key scopes. Failures are isolated: one
// broken target or Vault path never aborts the others.
func (r *Reconciler) Run(ctx context.Context, dryRun bool) *Report {
	start := time.Now()
	results := make([]TargetResult, len(r.Config.Expanded))

	sem := make(chan struct{}, r.Config.Sync.Concurrency)
	var wg stdsync.WaitGroup
	for i, t := range r.Config.Expanded {
		wg.Add(1)
		go func(i int, t config.Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = r.syncTarget(ctx, t, dryRun)
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

func (r *Reconciler) syncTarget(ctx context.Context, t config.Target, dryRun bool) TargetResult {
	log := r.Log.With("target", t.Ref.String())
	res := TargetResult{Target: t.Ref}

	if err := r.Store.ResolveTarget(ctx, &t.Ref); err != nil {
		res.Err = err
		log.Error("target resolution failed", "error", err)
		return res
	}
	res.Target = t.Ref

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
