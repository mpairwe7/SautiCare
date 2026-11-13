package demo

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer accepts TCP connections and counts them. With hang it holds
// each one open without a word, like a stalled server; otherwise it closes
// it at once. It returns a superuser-style DSN pointing at it.
func fakeServer(t *testing.T, hang bool) (string, *atomic.Int32) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		accepted atomic.Int32
		mu       sync.Mutex
		held     []net.Conn
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			if !hang {
				_ = c.Close()
				continue
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	return "postgres://postgres:x@" + ln.Addr().String() + "/postgres?sslmode=disable", &accepted
}

// The demo provisions server-global roles and databases: production is
// refused before any connection is opened (CodeRabbit PR #4).
func TestRunM0RefusesProductionBeforeConnecting(t *testing.T) {
	dsn, accepted := fakeServer(t, false)
	for _, c := range []struct{ processEnv, optEnv string }{
		{"production", ""},           // read from SAUTI_ENV
		{"test", "production"},       // passed by the caller
		{"production", "production"}, // both
	} {
		t.Setenv("SAUTI_ENV", c.processEnv)
		_, err := RunM0(context.Background(), Options{AdminDSN: dsn, Env: c.optEnv, RolePassword: "x", ConfigDir: "../../../config"})
		if err == nil || !strings.Contains(err.Error(), "production") || accepted.Load() != 0 {
			t.Fatalf("%+v: err = %v, connections = %d; want a refusal before connecting", c, err, accepted.Load())
		}
	}
	t.Setenv("SAUTI_ENV", "")
	if _, err := RunM0(context.Background(), Options{AdminDSN: dsn, RolePassword: "x"}); err == nil || accepted.Load() != 0 {
		t.Fatalf("unset SAUTI_ENV: err = %v, connections = %d; want a refusal before connecting", err, accepted.Load())
	}
}

// RunM0 bounds itself: a stalled server cannot hold a caller whose context
// has no deadline, as both e2e callers' does (CodeRabbit PR #4).
func TestRunM0HasItsOwnDeadline(t *testing.T) {
	dsn, accepted := fakeServer(t, true)
	t.Setenv("SAUTI_ENV", "test")
	old := runTimeout
	runTimeout = 300 * time.Millisecond
	t.Cleanup(func() { runTimeout = old })
	done := make(chan error, 1)
	go func() {
		_, err := RunM0(context.Background(), Options{AdminDSN: dsn, RolePassword: "x", ConfigDir: "../../../config"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || accepted.Load() != 1 {
			t.Fatalf("err = %v, connections = %d; want the run deadline on the stalled connection", err, accepted.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunM0 still waits on a stalled server: the run has no deadline")
	}
}

// Cleanup runs after the run's context may have ended, so it is detached
// from that context's cancellation but carries its own short deadline.
func TestCleanupContextIsDetachedAndBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cctx, ccancel := cleanupContext(ctx)
	defer ccancel()
	if cctx.Err() != nil {
		t.Fatal("cleanup context ended with the run")
	}
	if d, ok := cctx.Deadline(); !ok || time.Until(d) > cleanupTimeout {
		t.Fatalf("cleanup deadline = %v, %v; want at most %v", d, ok, cleanupTimeout)
	}
}

// CodeRabbit PR #10 (4557206001): step 4's ledger check looks for every
// number the demo uses — the caller's and each synthetic responder's — in
// the national form the ledger would show, not the caller's alone.
func TestLedgerCheckCoversEveryFixtureNumber(t *testing.T) {
	patterns := ledgerPhonePatterns()
	numbers := []string{demoPhone}
	for _, d := range demoResponders {
		numbers = append(numbers, d.phone)
	}
	for _, n := range numbers {
		national := strings.TrimPrefix(n, "+256")
		if !slices.Contains(patterns, "%"+national+"%") {
			t.Errorf("number %s is not searched for (patterns %v)", n, patterns)
		}
	}
}
