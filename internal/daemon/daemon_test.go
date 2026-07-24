package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	stdsync "sync"
	"syscall"
	"testing"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	syncpkg "github.com/vaivanov/vault-gitlab-operator/internal/sync"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// counterDaemon builds a daemon whose RunOnce just counts invocations.
func counterDaemon(interval time.Duration) (*Daemon, func() int) {
	var mu stdsync.Mutex
	runs := 0
	d := &Daemon{
		Log:      testLogger(),
		Interval: interval,
		RunOnce: func(context.Context) *syncpkg.Report {
			mu.Lock()
			runs++
			mu.Unlock()
			return &syncpkg.Report{}
		},
		Reload: func(context.Context) error { return nil },
	}
	return d, func() int {
		mu.Lock()
		defer mu.Unlock()
		return runs
	}
}

func TestDaemonTicks(t *testing.T) {
	d, runs := counterDaemon(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for runs() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop after cancel")
	}
	if got := runs(); got < 4 {
		t.Errorf("runs = %d, want >= 4 (immediate + ticks)", got)
	}
}

func TestDaemonRunsImmediatelyOnStart(t *testing.T) {
	d, runs := counterDaemon(time.Hour) // tick will never fire

	ctx, cancel := context.WithCancel(t.Context())
	go func() { _ = d.Start(ctx) }()

	deadline := time.Now().Add(time.Second)
	for runs() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if runs() != 1 {
		t.Errorf("runs = %d, want exactly 1 immediate run", runs())
	}
	if !d.Ready() {
		t.Error("Ready() = false after first run")
	}
}

func TestDaemonSIGHUPReload(t *testing.T) {
	var mu stdsync.Mutex
	reloads := 0
	d, runs := counterDaemon(time.Hour)
	d.Reload = func(context.Context) error {
		mu.Lock()
		reloads++
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	// Wait for the immediate run (signal handler is installed before it).
	deadline := time.Now().Add(time.Second)
	for runs() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		r := reloads
		mu.Unlock()
		if r == 1 && runs() >= 2 { // reload triggers an immediate run
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("reloads=%d runs=%d after SIGHUP", reloads, runs())
}

func TestDaemonReloadFailureKeepsRunning(t *testing.T) {
	d, runs := counterDaemon(50 * time.Millisecond)
	d.Reload = func(context.Context) error { return errors.New("bad config") }

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	deadline := time.Now().Add(time.Second)
	for runs() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}

	// The daemon must survive the failed reload and keep ticking.
	before := runs()
	deadline = time.Now().Add(2 * time.Second)
	for runs() <= before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runs() <= before {
		t.Fatal("daemon stopped ticking after failed reload")
	}
}

func TestHTTPEndpoints(t *testing.T) {
	d, _ := counterDaemon(time.Hour)
	d.Metrics = NewMetrics()
	handler := d.httpHandler()

	get := func(path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, string(body)
	}

	if code, _ := get("/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", code)
	}
	if code, _ := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz before first run = %d, want 503", code)
	}

	d.ready.Store(true)
	if code, _ := get("/readyz"); code != http.StatusOK {
		t.Errorf("/readyz after first run = %d, want 200", code)
	}

	d.Metrics.Observe(&syncpkg.Report{
		Targets: []syncpkg.TargetResult{{
			Target: config.TargetRef{Kind: config.KindProject, Ref: "a/b"},
			Actions: []syncpkg.Action{
				{Op: syncpkg.OpCreate},
				{Op: syncpkg.OpNoop},
			},
		}},
		Duration: 2 * time.Second,
	})
	code, body := get("/metrics")
	if code != http.StatusOK {
		t.Fatalf("/metrics = %d", code)
	}
	for _, want := range []string{
		`vgo_sync_runs_total{result="success"} 1`,
		`vgo_actions_total{op="create",target_kind="project"} 1`,
		`vgo_actions_total{op="noop",target_kind="project"} 1`,
		"vgo_last_sync_success_timestamp_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

func TestMetricsErrorRun(t *testing.T) {
	m := NewMetrics()
	m.Observe(&syncpkg.Report{
		Targets: []syncpkg.TargetResult{{
			Target: config.TargetRef{Kind: config.KindGroup, Ref: "g"},
			Actions: []syncpkg.Action{
				{Op: syncpkg.OpUpdate, Err: errors.New("400")},
			},
		}},
	})

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, mf := range families {
		for _, metric := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			switch {
			case mf.GetName() == "vgo_sync_runs_total" && labels["result"] == "error":
				found["error_run"] = metric.GetCounter().GetValue() == 1
			case mf.GetName() == "vgo_actions_total" && labels["op"] == "failed":
				found["failed_action"] = metric.GetCounter().GetValue() == 1
			}
		}
	}
	if !found["error_run"] || !found["failed_action"] {
		t.Errorf("metrics not recorded correctly: %+v", found)
	}
}

func TestHealthzFailsWhenLoopIsStale(t *testing.T) {
	d, _ := counterDaemon(time.Hour)
	d.StaleAfter = time.Minute
	handler := d.httpHandler()

	code := func() int {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return rec.Code
	}

	// No progress recorded yet (daemon not started): nothing to judge.
	if got := code(); got != http.StatusOK {
		t.Errorf("/healthz before start = %d, want 200", got)
	}

	d.markProgress()
	if got := code(); got != http.StatusOK {
		t.Errorf("/healthz after a fresh pass = %d, want 200", got)
	}

	d.lastProgress.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	if got := code(); got != http.StatusServiceUnavailable {
		t.Errorf("/healthz with a wedged loop = %d, want 503", got)
	}
}

func TestHealthzStalenessDisabledByDefault(t *testing.T) {
	d, _ := counterDaemon(time.Hour) // StaleAfter unset
	d.lastProgress.Store(time.Now().Add(-24 * time.Hour).UnixNano())

	rec := httptest.NewRecorder()
	d.httpHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz with StaleAfter=0 = %d, want 200 (check disabled)", rec.Code)
	}
}

func TestStartBaselinesTheStalenessClock(t *testing.T) {
	// A first pass that never returns must still trip /healthz.
	block := make(chan struct{})
	d := &Daemon{
		Log:        testLogger(),
		Interval:   time.Hour,
		StaleAfter: 50 * time.Millisecond,
		RunOnce: func(context.Context) *syncpkg.Report {
			<-block
			return &syncpkg.Report{}
		},
		Reload: func(context.Context) error { return nil },
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = d.Start(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !d.Healthy() {
			close(block)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(block)
	t.Fatal("daemon stayed healthy while its first pass hung")
}
