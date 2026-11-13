package ari

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/platform/sim"
)

// secret returns a random credential: no password literal in the repository.
func secret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func newPair(t *testing.T) (*sim.FakeARI, *Client) {
	t.Helper()
	pw := secret(t)
	f := sim.NewFakeARI("sauti", pw)
	t.Cleanup(f.Close)
	c, err := New(Config{BaseURL: f.URL(), User: "sauti", Password: pw, App: "sauti-ivr", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c.backoff = 10 * time.Millisecond
	return f, c
}

func stream(t *testing.T, c *Client) (<-chan Event, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Event, 64)
	go func() { _ = c.Events(ctx, func(e Event) { ch <- e }) }()
	t.Cleanup(cancel)
	return ch, cancel
}

func next(t *testing.T, ch <-chan Event, typ string) Event {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == typ {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event", typ)
		}
	}
}

func TestClientParsesStasisStartCallerID(t *testing.T) {
	f, c := newPair(t)
	ch, _ := stream(t, c)
	<-f.Connected()
	f.StartCall("chan-1", "+256772123456")
	e := next(t, ch, EventStasisStart)
	if e.ChannelID != "chan-1" || e.CallerNumber != "+256772123456" {
		t.Fatalf("event = %+v", e)
	}
	f.Press("chan-1", "4")
	if d := next(t, ch, EventDTMF); d.Digit != "4" || d.ChannelID != "chan-1" {
		t.Fatalf("dtmf = %+v", d)
	}
}

func TestClientAnswersPlaysAndHangsUp(t *testing.T) {
	f, c := newPair(t)
	ctx := context.Background()
	if err := c.Answer(ctx, "chan-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Play(ctx, "chan-1", "pb-1", []string{"sauti/en/danger", "sauti/en/press-1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Hangup(ctx, "chan-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /ari/channels/chan-1/answer", "POST /ari/channels/chan-1/play/pb-1", "DELETE /ari/channels/chan-1"}
	if got := f.Requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if p := f.Plays("chan-1"); len(p) != 1 || !reflect.DeepEqual(p[0], []string{"sauti/en/danger", "sauti/en/press-1"}) || !f.HungUp("chan-1") {
		t.Fatalf("plays %v hung up %v", p, f.HungUp("chan-1"))
	}
	if err := c.Play(ctx, "chan/../x", "pb", []string{"s"}); err == nil {
		t.Fatal("a channel ID with a path separator was sent")
	}
}

func TestClientReconnects(t *testing.T) {
	f, c := newPair(t)
	ch, _ := stream(t, c)
	<-f.Connected()
	f.DropStreams()
	<-f.Connected()
	f.StartCall("chan-2", "")
	if e := next(t, ch, EventStasisStart); e.ChannelID != "chan-2" || e.CallerNumber != "" {
		t.Fatalf("after reconnect: %+v", e)
	}
}

func TestClientRejectsWrongCredentials(t *testing.T) {
	f := sim.NewFakeARI("sauti", secret(t))
	defer f.Close()
	c, err := New(Config{BaseURL: f.URL(), User: "sauti", Password: secret(t), App: "sauti-ivr"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Answer(context.Background(), "chan-1"); err == nil {
		t.Fatal("401 accepted")
	}
}

// Sauti records nothing (Build Prompt §10.2, §10.5): the client has no way
// to start a recording, snoop a channel or stream media out. Its only bridge
// is a plain mixing bridge for the masked bridge (ADR-0028).
func TestNoRecordingEndpointsUsed(t *testing.T) {
	ty := reflect.TypeOf(&Client{})
	for i := range ty.NumMethod() {
		name := strings.ToLower(ty.Method(i).Name)
		for _, banned := range []string{"record", "snoop", "external"} {
			if strings.Contains(name, banned) {
				t.Errorf("client method %s", ty.Method(i).Name)
			}
		}
	}
}

func TestNewValidatesConfig(t *testing.T) {
	p := secret(t)
	for _, cfg := range []Config{
		{BaseURL: "", User: "u", Password: p, App: "a"},
		{BaseURL: "ftp://x", User: "u", Password: p, App: "a"},
		{BaseURL: "http://x", User: "", Password: p, App: "a"},
		{BaseURL: "http://x", User: "u", Password: "", App: "a"},
		{BaseURL: "http://x", User: "u", Password: p, App: ""},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
}

// Outbound alert calls (M2 Part B): originate dials an endpoint into this
// Stasis application with arguments, and the answered call's StasisStart
// carries them back. Nothing else (no recording) is asked of Asterisk.
func TestClientOriginatesAndReadsStasisArgs(t *testing.T) {
	f, c := newPair(t)
	ch, _ := stream(t, c)
	<-f.Connected()
	id, err := c.Originate(context.Background(), "PJSIP/+256772000301@sauti-trunk", "+256000000099", []string{"alert", "att-1"}, 30*time.Second)
	if err != nil || id == "" {
		t.Fatalf("originate: %q %v", id, err)
	}
	o := f.Originated()
	if len(o) != 1 || o[0].Get("endpoint") != "PJSIP/+256772000301@sauti-trunk" || o[0].Get("app") != "sauti-ivr" ||
		o[0].Get("appArgs") != "alert,att-1" || o[0].Get("timeout") != "30" || o[0].Get("callerId") != "+256000000099" {
		t.Fatalf("originate request %v", o)
	}
	f.Answered(id, "alert", "att-1")
	if e := next(t, ch, EventStasisStart); e.ChannelID != id || !reflect.DeepEqual(e.Args, []string{"alert", "att-1"}) {
		t.Fatalf("StasisStart %+v", e)
	}
	if _, err := c.Originate(context.Background(), "PJSIP/x", "", []string{"a,b"}, time.Second); err == nil {
		t.Fatal("an argument with a comma was sent")
	}
}

// The masked bridge (ADR-0028): a mixing bridge, the two legs added, then
// destroyed — no recording option is ever sent.
func TestClientMixingBridge(t *testing.T) {
	f, c := newPair(t)
	ctx := context.Background()
	if err := c.CreateBridge(ctx, "b-1"); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"chan-1", "chan-2"} {
		if err := c.AddToBridge(ctx, "b-1", ch); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.Bridged("b-1"); !reflect.DeepEqual(got, []string{"chan-1", "chan-2"}) {
		t.Fatalf("bridged %v", got)
	}
	if err := c.DestroyBridge(ctx, "b-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /ari/bridges/b-1", "POST /ari/bridges/b-1/addChannel", "POST /ari/bridges/b-1/addChannel", "DELETE /ari/bridges/b-1"}
	if got := f.Requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	if q := f.BridgeQuery("b-1"); q.Get("type") != "mixing" || len(q) != 1 {
		t.Fatalf("bridge created with %v, want only type=mixing", q)
	}
	if err := c.CreateBridge(ctx, "b/../x"); err == nil {
		t.Fatal("a bridge ID with a path separator was sent")
	}
}

// Part C review I2, I3: before a leg joins the bridge its caller ID becomes
// Sauti's number (the other party is never sent the real one as a
// connected-line update); a ringback tone plays while the other phone rings;
// only sound and the ring tone are ever played.
func TestClientCallerIDAndRingback(t *testing.T) {
	f, c := newPair(t)
	ctx := context.Background()
	if err := c.SetCallerID(ctx, "chan-1", "+256800100100"); err != nil {
		t.Fatal(err)
	}
	if got := f.CallerID("chan-1"); got != "+256800100100" {
		t.Fatalf("caller ID %q", got)
	}
	if err := c.SetCallerID(ctx, "chan-1", "x\\n"); err == nil {
		t.Fatal("a caller ID that is not a number was sent")
	}
	if err := c.Play(ctx, "chan-1", "pb-1", []string{"sauti/en/bridge-connecting", RingbackTone}); err != nil {
		t.Fatal(err)
	}
	if err := c.Play(ctx, "chan-1", "pb-2", []string{"recording:x"}); err == nil {
		t.Fatal("a non-sound media was played")
	}
	if EventChannelDestroyed != "ChannelDestroyed" {
		t.Fatal("ChannelDestroyed")
	}
}

// CodeRabbit PR #11 (4560453637): a new outbound leader asks Asterisk whether
// each channel it holds a coordinator line for still exists (GET
// /ari/channels/{id}): 404 means gone; any other failure is an error, never
// "gone" (a line in use must not be rung again).
func TestClientChannelExists(t *testing.T) {
	f, c := newPair(t)
	ctx := context.Background()
	id, err := c.Originate(ctx, "PJSIP/coordinators", "", []string{"escalation"}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := c.ChannelExists(ctx, id); err != nil || !ok {
		t.Fatalf("a placed call: %v %v", ok, err)
	}
	f.Answered("chan-9", "escalation")
	f.Hangup("chan-9") // the far end hung up
	if err := c.Hangup(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{id, "chan-9", "never-seen"} {
		if ok, err := c.ChannelExists(ctx, ch); err != nil || ok {
			t.Fatalf("%s: %v %v, want gone", ch, ok, err)
		}
	}
	if _, err := c.ChannelExists(ctx, "chan/../x"); err == nil {
		t.Fatal("a channel ID with a path separator was sent")
	}
	wrong, err := New(Config{BaseURL: f.URL(), User: "sauti", Password: secret(t), App: "sauti-ivr"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := wrong.ChannelExists(ctx, "chan-9"); err == nil || ok {
		t.Fatalf("a refused request read as an answer: %v %v", ok, err)
	}
}

// ListChannels names each channel's Stasis application, so ivr-controller can
// find the calls parked in its own application after a reconnect.
func TestClientListsChannelsWithTheirApplication(t *testing.T) {
	f, c := newPair(t)
	f.StartCall("chan-9", "+256772123456")
	got, err := c.ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Channel{{ID: "chan-9", App: "sauti-ivr"}}) {
		t.Fatalf("channels %+v", got)
	}
}

// Stream calls connected on every (re)connect, before that connection's
// first event is handled.
func TestClientStreamReportsEachConnect(t *testing.T) {
	f, c := newPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan string, 64)
	go func() {
		_ = c.Stream(ctx, func() { ch <- "connected" }, func(e Event) { ch <- e.Type })
	}()
	<-f.Connected()
	f.DropStreams()
	<-f.Connected()
	f.StartCall("chan-2", "")
	var seen []string
	for len(seen) < 3 {
		select {
		case s := <-ch:
			seen = append(seen, s)
		case <-time.After(3 * time.Second):
			t.Fatalf("seen %v", seen)
		}
	}
	if !reflect.DeepEqual(seen, []string{"connected", "connected", EventStasisStart}) {
		t.Fatalf("seen %v", seen)
	}
}

// A handed-over call shows the coordinator "SAUTI" and the reference as the
// caller name (rule 5); a name is letters, digits and spaces only.
func TestClientSetsCallerName(t *testing.T) {
	f, c := newPair(t)
	ctx := context.Background()
	if err := c.SetCallerName(ctx, "chan-1", "SAUTI 123 456 7894"); err != nil {
		t.Fatal(err)
	}
	if got := f.CallerName("chan-1"); got != "SAUTI 123 456 7894" {
		t.Fatalf("caller name %q", got)
	}
	for _, bad := range []string{"", "x\"<1>", "SAUTI\n"} {
		if err := c.SetCallerName(ctx, "chan-1", bad); err == nil {
			t.Fatalf("name %q was sent", bad)
		}
	}
}
