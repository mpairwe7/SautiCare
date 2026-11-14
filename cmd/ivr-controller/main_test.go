package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
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

// CodeRabbit PR #11 (4560453641): the no-input time of answered forwarding
// and coordinator calls is configurable within bounds; unset, the default.
func TestNoInputSetting(t *testing.T) {
	for v, want := range map[string]time.Duration{"": ivr.DefaultNoInput, "90s": 90 * time.Second, "5m": 5 * time.Minute} {
		if got, err := noInputSetting(v); err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", v, got, err, want)
		}
	}
	for _, v := range []string{"10s", "1h", "two minutes", "-2m"} {
		if _, err := noInputSetting(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

// termARI and termStore are just enough of Asterisk and the store for one
// answered alert call; the fake locks what the call's timer goroutine writes.
type termARI struct {
	mu      sync.Mutex
	plays   int
	hangups int
}

func (a *termARI) Originate(context.Context, string, string, []string, time.Duration) (string, error) {
	return "", nil
}
func (a *termARI) Answer(context.Context, string) error { return nil }
func (a *termARI) Play(context.Context, string, string, []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.plays++
	return nil
}
func (a *termARI) played() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.plays
}
func (a *termARI) StopPlayback(context.Context, string) error { return nil }
func (a *termARI) Hangup(context.Context, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hangups++
	return nil
}
func (a *termARI) hungUp() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hangups
}

type termStore struct{ ivr.AlertStore }

func (termStore) AlertForAttempt(context.Context, uuid.UUID) (dispatch.ResponderAlert, uuid.UUID, error) {
	return dispatch.ResponderAlert{AlertID: uuid.NewV7(), Facts: dispatch.MessageFacts{Reference: "1234567894", Severity: "red"}}, uuid.NewV7(), nil
}

type nopOpener struct{}

func (nopOpener) Open(context.Context, crypto.Sealed, crypto.AAD) ([]byte, error) { return nil, nil }

// PR #14 review: a leader term that ends while its stream is down (waiting to
// reconnect) still stops its calls' timers, so no timer of the old term plays
// or closes a call afterwards.
func TestATermEndingWhileDisconnectedStopsTheCallTimers(t *testing.T) {
	a := &termARI{}
	calls, err := ivr.NewAlertCalls(ivr.AlertCallsConfig{ARI: a, Store: termStore{}, Opener: nopOpener{}, Dial: "PJSIP/{number}@trunk",
		After: 20 * time.Second, NoInput: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	calls.Handle(ctx, ari.Event{Type: ari.EventStasisStart, ChannelID: "out-1", Args: []string{"alert", uuid.NewV7().String()}})
	calls.Handle(ctx, ari.Event{Type: ari.EventPlaybackFinished, ChannelID: "out-1", PlaybackID: "out-1-a1"}) // the menu heard: its timer runs
	var down atomic.Bool
	st := &outboundStatus{connected: down.Load}
	lctx, end := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- outboundTerm(func(c context.Context, _ func(ari.Event)) error { <-c.Done(); return c.Err() }, st,
			ivr.Outbound{Alerts: calls})(lctx)
	}()
	end() // the term ends while the stream is still down
	<-done
	time.Sleep(300 * time.Millisecond)
	if n := a.played(); n != 1 {
		t.Fatalf("plays %d: a timer of the ended term played", n)
	}
}

// PR #14 review (round 4): leadership is lost while the term still waits for
// a loop to stop, and another process may already lead. An alert call's
// timer that fires during that wait does nothing: no prompt, no hang-up.
func TestATimerFiringWhileTheTermWaitsForItsLoopsDoesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &termARI{}
		calls, err := ivr.NewAlertCalls(ivr.AlertCallsConfig{ARI: a, Store: termStore{}, Opener: nopOpener{}, Dial: "PJSIP/{number}@trunk",
			After: 20 * time.Second, NoInput: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		up := func() bool { return true }
		answered, release := make(chan struct{}), make(chan struct{})
		events := func(c context.Context, h func(ari.Event)) error {
			h(ari.Event{Type: ari.EventStasisStart, ChannelID: "out-1", Args: []string{"alert", uuid.NewV7().String()}})
			h(ari.Event{Type: ari.EventPlaybackFinished, ChannelID: "out-1", PlaybackID: "out-1-a1"}) // the menu heard: its timer runs
			close(answered)
			<-c.Done()
			return c.Err()
		}
		slowLoop := func(c context.Context) error { <-c.Done(); <-release; return nil }
		lctx, lost := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- outboundTerm(events, &outboundStatus{connected: up}, ivr.Outbound{Alerts: calls}, slowLoop)(lctx)
		}()
		<-answered
		lost()                  // the database lock is lost
		time.Sleep(time.Second) // the no-input timer fires while the loop is still stopping
		n, hangups := a.played(), a.hungUp()
		close(release)
		<-done
		if n != 1 || hangups != 0 {
			t.Fatalf("plays %d hangups %d: a timer acted after leadership was lost", n, hangups)
		}
	})
}
