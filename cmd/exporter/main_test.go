package main

import (
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/jalet/scm-metrics-exporter/internal/config"
)

// TestOpenRemediationStoreDisabled verifies that a disabled lifecycle config never attempts
// a connection: it must return a nil store and connected=false without touching ValkeyAddr.
func TestOpenRemediationStoreDisabled(t *testing.T) {
	st, connected := openRemediationStore(config.LifecycleConfig{Enabled: false, ValkeyAddr: "127.0.0.1:1"})
	if st != nil {
		t.Fatalf("store = %v, want nil (lifecycle disabled)", st)
	}
	if connected {
		t.Fatalf("connected = true, want false (lifecycle disabled)")
	}
}

// TestOpenRemediationStoreUnreachable proves that a Valkey reachability failure is
// swallowed, not fatal: it must return a nil store and connected=false. It does not run in
// its own goroutine with a manual timeout -- go-redis retries the dial a handful of times
// with backoff against a refused connection (a couple of seconds here), then Ping returns
// the error; there's no indefinite blocking, so the surrounding `go test` default timeout is
// sufficient to catch a regression that made this hang. This is the regression guard for the
// fix that keeps a Valkey outage from taking down the whole collection Job before core
// metrics (open findings, posture, workflow runs) push.
func TestOpenRemediationStoreUnreachable(t *testing.T) {
	st, connected := openRemediationStore(config.LifecycleConfig{
		Enabled:    true,
		ValkeyAddr: "127.0.0.1:1", // nothing listens here
	})
	if st != nil {
		t.Fatalf("store = %v, want nil (unreachable Valkey must not return a store)", st)
	}
	if connected {
		t.Fatalf("connected = true, want false (unreachable Valkey must not be reported as connected)")
	}
}

// TestOpenRemediationStoreConnects verifies the happy path against a real (miniredis)
// server: lifecycle enabled and reachable must return a non-nil store with connected=true.
func TestOpenRemediationStoreConnects(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	st, connected := openRemediationStore(config.LifecycleConfig{Enabled: true, ValkeyAddr: mr.Addr()})
	if st == nil {
		t.Fatal("store = nil, want non-nil (miniredis is reachable)")
	}
	defer func() { _ = st.Close() }()
	if !connected {
		t.Fatal("connected = false, want true (miniredis is reachable)")
	}
}
