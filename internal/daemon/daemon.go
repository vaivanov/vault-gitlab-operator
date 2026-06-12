// Package daemon runs the reconcile loop continuously: an immediate
// first pass, then ticks at a configurable interval with jitter, with
// graceful shutdown on SIGTERM/SIGINT, config reload on SIGHUP and an
// HTTP endpoint trio (/healthz, /readyz, /metrics).
package daemon

import (
	"context"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	syncpkg "github.com/vaivanov/vault-gitlab-operator/internal/sync"
)

type Daemon struct {
	Log      *slog.Logger
	Interval time.Duration
	Jitter   time.Duration

	// RunOnce performs one reconcile pass.
	RunOnce func(ctx context.Context) *syncpkg.Report
	// Reload re-reads the config; called on SIGHUP. A returned error
	// keeps the previous configuration running.
	Reload func(ctx context.Context) error

	Metrics *Metrics
	// ListenAddr serves /healthz, /readyz and /metrics when non-empty.
	ListenAddr string

	ready atomic.Bool
}

// Ready reports whether at least one reconcile pass has completed.
func (d *Daemon) Ready() bool { return d.ready.Load() }

// Start blocks until ctx is cancelled.
func (d *Daemon) Start(ctx context.Context) error {
	if d.ListenAddr != "" {
		srv := &http.Server{
			Addr:              d.ListenAddr,
			Handler:           d.httpHandler(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			d.Log.Info("http server listening", "addr", d.ListenAddr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				d.Log.Error("http server failed", "error", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
	}

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	run := func() {
		report := d.RunOnce(ctx)
		if d.Metrics != nil {
			d.Metrics.Observe(report)
		}
		d.ready.Store(true)
	}

	d.Log.Info("daemon started", "interval", d.Interval, "jitter", d.Jitter)
	run()

	for {
		timer := time.NewTimer(d.nextDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			d.Log.Info("daemon stopping")
			return nil
		case <-hup:
			timer.Stop()
			d.Log.Info("SIGHUP received, reloading config")
			if err := d.Reload(ctx); err != nil {
				d.Log.Error("config reload failed, keeping previous config", "error", err)
				continue
			}
			d.Log.Info("config reloaded")
			run()
		case <-timer.C:
			run()
		}
	}
}

func (d *Daemon) nextDelay() time.Duration {
	delay := d.Interval
	if d.Jitter > 0 {
		delay += time.Duration(rand.Int63n(int64(d.Jitter)))
	}
	return delay
}

func (d *Daemon) httpHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !d.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("waiting for first reconcile\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	if d.Metrics != nil {
		mux.Handle("/metrics", metricsHandler(d.Metrics))
	}
	return mux
}
