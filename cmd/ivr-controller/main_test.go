package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// over), so the calls the leader places and the events of their answers meet
// in one process; the subscription ends with the term.
func TestOutboundEventsOnlyWhileLeader(t *testing.T) {
	subscribed := make(chan context.Context, 1)
	events := func(ctx context.Context, _ func(ari.Event)) error {
		subscribed <- ctx
		<-ctx.Done()
		return ctx.Err()
	}
	loopRan := make(chan struct{}, 2)
	loop := func(ctx context.Context) error {
		loopRan <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	lctx, end := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- outboundTerm(events, ivr.Outbound{}, loop, loop)(lctx) }()
	sub := <-subscribed
	<-loopRan
	<-loopRan
	end()
	<-done
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("the subscription outlived the leadership term")
	}
}
