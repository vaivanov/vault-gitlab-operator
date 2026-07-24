package main

import (
	"context"
	"os/signal"
	stdsync "sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/vaivanov/vault-gitlab-operator/internal/daemon"
	syncpkg "github.com/vaivanov/vault-gitlab-operator/internal/sync"
)

func newDaemonCmd(flags *rootFlags) *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the reconcile loop continuously",
		Long: "Runs an immediate reconcile pass, then repeats on the configured " +
			"interval (sync.interval + jitter). SIGHUP reloads the config " +
			"(interval changes require a restart); SIGTERM stops gracefully.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := setupLogger(flags)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// current holds the active reconciler plus the cancel func of
			// its Vault KeepAlive goroutine; SIGHUP swaps it atomically.
			type runtime struct {
				rec    *syncpkg.Reconciler
				cancel context.CancelFunc
			}
			var mu stdsync.Mutex
			var current *runtime

			build := func(ctx context.Context) (*runtime, error) {
				rec, vaultClient, _, err := buildReconciler(ctx, flags, log)
				if err != nil {
					return nil, err
				}
				kaCtx, cancel := context.WithCancel(ctx)
				go vaultClient.KeepAlive(kaCtx, log)
				return &runtime{rec: rec, cancel: cancel}, nil
			}

			current, err = build(ctx)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}
			interval := current.rec.Config.Sync.Interval.Std()
			jitter := current.rec.Config.Sync.Jitter.Std()

			d := &daemon.Daemon{
				Log:        log,
				Interval:   interval,
				Jitter:     jitter,
				Metrics:    daemon.NewMetrics(),
				ListenAddr: listen,
				// A pass is bounded by sync.timeout and one starts every
				// interval+jitter, so two full cycles plus a timeout
				// without a completed pass means the loop is wedged.
				StaleAfter: 2*(interval+jitter) + current.rec.Config.Sync.PassTimeout(),
				RunOnce: func(ctx context.Context) *syncpkg.Report {
					mu.Lock()
					rt := current
					mu.Unlock()
					return runPass(ctx, rt.rec, false)
				},
				Reload: func(ctx context.Context) error {
					next, err := build(ctx)
					if err != nil {
						return err
					}
					mu.Lock()
					prev := current
					current = next
					mu.Unlock()
					prev.cancel()
					return nil
				},
			}
			return d.Start(ctx)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":8080",
		"address for /healthz, /readyz and /metrics (empty to disable)")
	return cmd
}
