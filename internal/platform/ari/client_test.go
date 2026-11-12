package ari

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/platform/sim"
)

func newPair(t *testing.T) (*sim.FakeARI, *Client) {
	t.Helper()
	f := sim.NewFakeARI("sauti", "s3cret-test")
	t.Cleanup(f.Close)
	c, err := New(Config{BaseURL: f.URL(), User: "sauti", Password: "s3cret-test", App: "sauti-ivr", Timeout: 2 * time.Second})
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
	f := sim.NewFakeARI("sauti", "right")
	defer f.Close()
	c, err := New(Config{BaseURL: f.URL(), User: "sauti", Password: "wrong", App: "sauti-ivr"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Answer(context.Background(), "chan-1"); err == nil {
		t.Fatal("401 accepted")
	}
}

// Sauti records nothing (Build Prompt §10.2): the client has no way to start
// a recording, snoop a channel or build a bridge.
func TestNoRecordingEndpointsUsed(t *testing.T) {
	ty := reflect.TypeOf(&Client{})
	for i := range ty.NumMethod() {
		name := strings.ToLower(ty.Method(i).Name)
		for _, banned := range []string{"record", "snoop", "bridge", "external"} {
			if strings.Contains(name, banned) {
				t.Errorf("client method %s", ty.Method(i).Name)
			}
		}
	}
}

func TestNewValidatesConfig(t *testing.T) {
	for _, cfg := range []Config{
		{BaseURL: "", User: "u", Password: "p", App: "a"},
		{BaseURL: "ftp://x", User: "u", Password: "p", App: "a"},
		{BaseURL: "http://x", User: "", Password: "p", App: "a"},
		{BaseURL: "http://x", User: "u", Password: "", App: "a"},
		{BaseURL: "http://x", User: "u", Password: "p", App: ""},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
}
