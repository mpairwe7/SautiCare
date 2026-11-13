package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/health"
	"sautihealth.com/sauti/internal/platform/ivr"
)

type stream bool

func (s stream) EventsConnected() bool { return bool(s) }

// I4: the voice line is not ready while the ARI event stream is down (no
// call can be served; the dialplan's fallback takes them), even if the
// database is up.
func TestReadinessIncludesTheARIEventStream(t *testing.T) {
	dbUp := func(context.Context) error { return nil }
	for _, tc := range []struct {
		events bool
		code   int
		state  string
	}{{true, http.StatusOK, "ok"}, {false, http.StatusServiceUnavailable, "fail"}} {
		rec := httptest.NewRecorder()
		health.Handler(nil, readiness(dbUp, stream(tc.events))).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		var body struct {
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.code || body.Checks["ari_events"] != tc.state || body.Checks["operational_db"] != "ok" {
			t.Fatalf("events up=%v: %d %v", tc.events, rec.Code, body.Checks)
		}
	}
}

// The outbound application's events are taken only during a leadership term
// (Asterisk gives an application one websocket; a newer connection takes it
// over), and calls are placed only while that stream is connected
// (CodeRabbit PR #11 4558029983): a call answered while it is down would
// never reach Sauti. Readiness fails on the leader while its stream is down.
// The term returns only once its subscription and both loops have exited
// (CodeRabbit PR #11 4560453586): a later term in this process never runs
// beside a stale websocket.
func TestOutboundCallsOnlyWhileLeaderAndConnected(t *testing.T) {
	outboundPoll = 5 * time.Millisecond
	var up atomic.Bool
	subscribed := make(chan context.Context, 1)
	subDone := make(chan struct{})
	events := func(ctx context.Context, _ func(ari.Event)) error {
		defer close(subDone)
		subscribed <- ctx
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // closing the websocket takes a moment
		return ctx.Err()
	}
	type loopRun struct{ started, exited chan struct{} }
	runs := make(chan loopRun, 10)
	loop := func(ctx context.Context) error {
		r := loopRun{make(chan struct{}), make(chan struct{})}
		runs <- r
		close(r.started)
		<-ctx.Done()
		close(r.exited)
		return ctx.Err()
	}
	// both waits for the two loops of one connection to start.
	both := func() [2]loopRun {
		t.Helper()
		var out [2]loopRun
		for i := range out {
			out[i] = within(t, runs, "a loop to start")
		}
		return out
	}
	st := &outboundStatus{connected: up.Load}
	check := outboundReady(st)
	if err := check(context.Background()); err != nil {
		t.Fatalf("not leading: %v", err)
	}
	lctx, end := context.WithCancel(context.Background())
	defer end()
	done := make(chan error, 1)
	go func() { done <- outboundTerm(events, st, ivr.Outbound{}, loop, loop)(lctx) }()
	within(t, subscribed, "the subscription")
	time.Sleep(50 * time.Millisecond)
	if len(runs) != 0 {
		t.Fatal("calls placed before the stream connected")
	}
	if err := check(context.Background()); err == nil {
		t.Fatal("the leader is ready with its outbound stream down")
	}
	up.Store(true)
	first := both()
	if err := check(context.Background()); err != nil {
		t.Fatalf("connected leader not ready: %v", err)
	}
	up.Store(false)
	for i, r := range first {
		within(t, r.exited, fmt.Sprintf("loop %d to stop after the stream dropped", i))
	}
	up.Store(true)
	second := both()
	end()
	within(t, done, "the term to end")
	select {
	case <-subDone:
	default:
		t.Fatal("the term returned while its event subscription was still running")
	}
	for i, r := range second {
		select {
		case <-r.exited:
		default:
			t.Fatalf("the term returned while loop %d was still running", i)
		}
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("after the term: %v", err)
	}
}

// within waits a bounded time for one value from ch.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}
