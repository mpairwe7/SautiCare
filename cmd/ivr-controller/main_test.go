package main

import (
	"context"
	"encoding/json"
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
func TestOutboundCallsOnlyWhileLeaderAndConnected(t *testing.T) {
	outboundPoll = 5 * time.Millisecond
	var up atomic.Bool
	subscribed := make(chan context.Context, 1)
	events := func(ctx context.Context, _ func(ari.Event)) error {
		subscribed <- ctx
		<-ctx.Done()
		return ctx.Err()
	}
	var running atomic.Int32
	starts := make(chan struct{}, 10)
	loop := func(ctx context.Context) error {
		running.Add(1)
		starts <- struct{}{}
		<-ctx.Done()
		running.Add(-1)
		return ctx.Err()
	}
	st := &outboundStatus{connected: up.Load}
	check := outboundReady(st)
	if err := check(context.Background()); err != nil {
		t.Fatalf("not leading: %v", err)
	}
	lctx, end := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- outboundTerm(events, st, ivr.Outbound{}, loop, loop)(lctx) }()
	sub := <-subscribed
	time.Sleep(50 * time.Millisecond)
	if running.Load() != 0 {
		t.Fatal("calls placed before the stream connected")
	}
	if err := check(context.Background()); err == nil {
		t.Fatal("the leader is ready with its outbound stream down")
	}
	up.Store(true)
	<-starts
	<-starts
	if err := check(context.Background()); err != nil {
		t.Fatalf("connected leader not ready: %v", err)
	}
	up.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for running.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if running.Load() != 0 {
		t.Fatal("calls still placed after the stream dropped")
	}
	up.Store(true)
	<-starts
	<-starts
	end()
	<-done
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("the subscription outlived the leadership term")
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("after the term: %v", err)
	}
}
