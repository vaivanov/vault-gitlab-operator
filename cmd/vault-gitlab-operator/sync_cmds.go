package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	"github.com/vaivanov/vault-gitlab-operator/internal/gitlab"
	"github.com/vaivanov/vault-gitlab-operator/internal/sync"
	"github.com/vaivanov/vault-gitlab-operator/internal/vault"
)

// buildReconciler wires config, the Vault secret source (with version
// cache) and the GitLab store into a ready-to-run reconciler. The Vault
// client is returned too so daemon mode can keep its token alive.
func buildReconciler(ctx context.Context, flags *rootFlags, log *slog.Logger) (*sync.Reconciler, *vault.Client, *config.Config, error) {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, w := range cfg.Warnings {
		log.Warn(w)
	}

	vaultClient, err := vault.New(cfg.Vault)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := vaultClient.Login(ctx); err != nil {
		return nil, nil, nil, err
	}

	store, err := gitlab.New(cfg.GitLab)
	if err != nil {
		return nil, nil, nil, err
	}

	return &sync.Reconciler{
		Config:  cfg,
		Secrets: vault.NewCached(vaultClient),
		Store:   store,
		Log:     log,
	}, vaultClient, cfg, nil
}

// runPass executes one reconcile pass bounded by sync.timeout, so an
// unresponsive Vault or GitLab cannot hang the process indefinitely.
func runPass(ctx context.Context, r *sync.Reconciler, dryRun bool) *sync.Report {
	if timeout := r.Config.Sync.PassTimeout(); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.Run(ctx, dryRun)
}

func newOnceCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "once",
		Short: "Run a single reconcile pass and exit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := setupLogger(flags)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			r, _, _, err := buildReconciler(ctx, flags, log)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}

			report := runPass(ctx, r, false)
			report.Render(cmd.OutOrStdout(), false)
			if report.HasErrors() {
				return &exitError{code: exitSyncErrors, err: fmt.Errorf("sync finished with errors")}
			}
			return nil
		},
	}
}

func newDiffCmd(flags *rootFlags) *cobra.Command {
	var exitNonzeroOnDiff bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show what a sync would change without writing anything",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := setupLogger(flags)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			r, _, _, err := buildReconciler(ctx, flags, log)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}

			report := runPass(ctx, r, true)
			report.Render(cmd.OutOrStdout(), true)
			if report.HasErrors() {
				return &exitError{code: exitSyncErrors, err: fmt.Errorf("diff finished with errors")}
			}
			if exitNonzeroOnDiff && report.HasChanges() {
				return &exitError{code: exitDiffPending, err: fmt.Errorf("changes pending")}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&exitNonzeroOnDiff, "exit-nonzero-on-diff", false,
		fmt.Sprintf("exit with code %d when changes are pending (for CI gating)", exitDiffPending))
	return cmd
}
