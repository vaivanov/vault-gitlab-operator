package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDaemonEndToEnd(t *testing.T) {
	f := newE2EFixture(t)

	// Rewrite the config with a short interval for the test.
	data, err := os.ReadFile(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	fast := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeFile(fast, string(data)+"\nsync: {interval: 50ms}\n"); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() { done <- run([]string{"daemon", "--listen", "", "-c", fast}) }()

	// Wait for the first reconcile to create the variable, then for at
	// least one more converged pass.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.vars)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	created := len(f.vars)
	f.mu.Unlock()
	if created != 1 {
		t.Fatalf("daemon did not create the variable: vars=%d", created)
	}

	time.Sleep(150 * time.Millisecond) // a few more ticks: must stay idempotent
	f.mu.Lock()
	writes := f.writes
	f.mu.Unlock()
	if writes != 1 {
		t.Errorf("daemon repeated writes on converged state: %d", writes)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("daemon exit = %d, want %d", code, exitOK)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop on SIGINT")
	}
}

func TestDaemonHTTPEndpointsServe(t *testing.T) {
	f := newE2EFixture(t)

	// Pick a free port by binding and immediately releasing it.
	probe := httptest.NewServer(http.NotFoundHandler())
	addr := probe.Listener.Addr().String()
	probe.Close()

	done := make(chan int, 1)
	go func() { done <- run([]string{"daemon", "--listen", addr, "-c", f.configPath}) }()
	t.Cleanup(func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})

	var lastErr error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://%s/readyz", addr))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				// Metrics must serve too.
				mResp, err := http.Get(fmt.Sprintf("http://%s/metrics", addr))
				if err != nil {
					t.Fatal(err)
				}
				mResp.Body.Close()
				if mResp.StatusCode != http.StatusOK {
					t.Fatalf("/metrics = %d", mResp.StatusCode)
				}
				return
			}
			lastErr = fmt.Errorf("readyz status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon never became ready: %v", lastErr)
}
