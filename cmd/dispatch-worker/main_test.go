package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/location"
)

// Production refuses to start until the key provider exists (P0): the
// worker opens responders' numbers. With Africa's Talking credentials the
// worker sends real SMS; without them, development and tests simulate
// delivery, logging identifiers only.
func TestPickMessenger(t *testing.T) {
	built := false
	real := func() (messenger, error) { built = true; return simMessenger{log: slog.Default()}, nil }
	if _, err := pickMessenger("production", "key", real, slog.Default()); err == nil || built {
		t.Fatal("production accepted")
	}
	if _, err := pickMessenger("dev", "key", real, slog.Default()); err != nil || !built {
		t.Fatalf("credentials given: built=%v %v", built, err)
	}
	var buf bytes.Buffer
	m, err := pickMessenger("dev", "", real, slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := m.Deliver(context.Background(), dispatch.Delivery{AttemptID: uuid.NewV7(), AlertID: uuid.NewV7(),
		IncidentID: uuid.NewV7(), ResponderID: uuid.NewV7(), Role: "vht", Purpose: dispatch.PurposeAlert, Channel: dispatch.ChannelSMS})
	if err != nil || !strings.HasPrefix(ref, "sim-") || !strings.Contains(buf.String(), `"purpose":"alert"`) {
		t.Fatalf("simulated delivery: %q %v %s", ref, err, buf.String())
	}
	if _, err := m.Notify(context.Background(), dispatch.Notice{AttemptID: uuid.NewV7(), IncidentID: uuid.NewV7(), Kind: dispatch.CallerHelperOnTheWay}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remind(context.Background(), dispatch.Reminder{ResponderID: uuid.NewV7(), Until: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// Review I8: the leader is not ready when its last successful pass is older
// than the stall limit (a hang, or the database unreachable); a standby
// instance is ready (it is not dispatching, and it can take over).
func TestReadinessFailsWhenTheLeaderStalls(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		leading bool
		last    time.Time
		ok      bool
	}{
		{false, time.Time{}, true},
		{true, now.Add(-10 * time.Second), true},
		{true, now.Add(-3 * time.Minute), false},
		{true, time.Time{}, false},
	} {
		if err := dispatchLoopReady(tc.leading, tc.last, now); (err == nil) != tc.ok {
			t.Errorf("leading=%v last=%v: %v", tc.leading, tc.last, err)
		}
	}
}

// CodeRabbit PR #10 (4557205801): whatever ends the run — a signal, the
// admin listener failing, or the leader returning — the leader is cancelled
// and has returned before run's deferred pool.Close, which waits for the
// leader's lock connection while the leader waits for a cancellation.
func TestAwaitStopsTheLeaderBeforeThePoolCloses(t *testing.T) {
	listen := errors.New("listen tcp 127.0.0.1:9102: bind: address already in use")
	poison := errors.New("leader stopped on its own")
	for name, tc := range map[string]struct {
		signal         bool
		admin, leader  error
		want           error
		leaderOnItsOwn bool
	}{
		"admin listener fails": {admin: listen, want: listen},
		"signal":               {signal: true},
		"leader returns":       {leader: poison, want: poison, leaderOnItsOwn: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			adminErr, leaderErr := make(chan error, 1), make(chan error, 1)
			leaderReturned := make(chan struct{})
			go func() { // RunAsLeader: holds its lock connection until ctx ends
				if tc.leaderOnItsOwn {
					close(leaderReturned)
					leaderErr <- tc.leader
					return
				}
				<-ctx.Done()
				close(leaderReturned)
				leaderErr <- ctx.Err()
			}()
			switch {
			case tc.admin != nil:
				adminErr <- tc.admin
			case tc.signal:
				stop()
			}
			done := make(chan error, 1)
			go func() { done <- await(ctx, stop, adminErr, leaderErr) }()
			select {
			case err := <-done:
				if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
					t.Fatalf("await = %v, want %v", err, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("await hung")
			}
			select {
			case <-leaderReturned:
			default:
				t.Fatal("await returned while the leader still holds its lock connection: pool.Close would hang")
			}
		})
	}
}

// CodeRabbit PR #10 (4557205811): the admin listener bounds header reads,
// whole requests, responses (above the 2 s health checks) and idle
// keep-alive connections, wherever SAUTI_ADMIN_ADDR exposes it.
func TestAdminServerBoundsConnections(t *testing.T) {
	s := adminServer("127.0.0.1:0", http.NewServeMux())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.IdleTimeout <= 0 || s.WriteTimeout <= 2*time.Second {
		t.Fatalf("timeouts: header %v read %v write %v idle %v", s.ReadHeaderTimeout, s.ReadTimeout, s.WriteTimeout, s.IdleTimeout)
	}
}

// CodeRabbit PR #11 (4560453566): with Africa's Talking SMS on, the gazetteer
// read runs before the health endpoints exist; a stalled database fails the
// start after snapshotTimeout instead of holding it until a signal.
func TestTheStartupGazetteerReadIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		_, err := gazetteerInForce(t.Context(), func(ctx context.Context) (location.File, error) {
			<-ctx.Done()
			return location.File{}, ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != snapshotTimeout {
			t.Fatalf("stalled read: %v after %v, want the deadline after %v", err, time.Since(start), snapshotTimeout)
		}
		want := location.File{Version: "v1"}
		if got, err := gazetteerInForce(t.Context(), func(context.Context) (location.File, error) { return want, nil }); err != nil || got.Version != "v1" {
			t.Fatalf("read %+v %v", got, err)
		}
	})
}
