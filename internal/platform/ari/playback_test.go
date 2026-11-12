package ari

import (
	"context"
	"slices"
	"testing"
	"time"
)

// I2: a prompt can be stopped (barge-in) and its start is an event. The
// fake models Asterisk's per-channel playback queue: a second prompt starts
// only when the first finishes or is stopped.
func TestClientStopsPlaybackAndSeesItStart(t *testing.T) {
	f, c := newPair(t)
	f.SetPlaybackDuration(-1) // prompts play until stopped or finished by the test
	ch, _ := stream(t, c)
	<-f.Connected()
	ctx := context.Background()
	if err := c.Play(ctx, "chan-1", "pb-1", []string{"sauti/en/danger"}); err != nil {
		t.Fatal(err)
	}
	if e := next(t, ch, EventPlaybackStarted); e.PlaybackID != "pb-1" || e.ChannelID != "chan-1" {
		t.Fatalf("started = %+v", e)
	}
	if err := c.Play(ctx, "chan-1", "pb-2", []string{"sauti/en/category"}); err != nil {
		t.Fatal(err)
	}
	if err := c.StopPlayback(ctx, "pb-1"); err != nil {
		t.Fatal(err)
	}
	if e := next(t, ch, EventPlaybackFinished); e.PlaybackID != "pb-1" {
		t.Fatalf("finished = %+v", e)
	}
	if e := next(t, ch, EventPlaybackStarted); e.PlaybackID != "pb-2" {
		t.Fatalf("queued prompt started = %+v", e)
	}
	f.FinishPlayback("pb-2")
	if e := next(t, ch, EventPlaybackFinished); e.PlaybackID != "pb-2" {
		t.Fatalf("finished = %+v", e)
	}
	if err := c.StopPlayback(ctx, "../x"); err == nil {
		t.Fatal("a playback ID with a path separator was sent")
	}
	if got := f.Requests(); !slices.Contains(got, "DELETE /ari/playbacks/pb-1") {
		t.Fatalf("requests = %v", got)
	}
}

// I4: a call the controller cannot serve leaves Stasis for the dialplan's
// fallback context (a recorded message, then a coordinator).
func TestClientContinuesToTheDialplan(t *testing.T) {
	f, c := newPair(t)
	ch, _ := stream(t, c)
	<-f.Connected()
	if err := c.Continue(context.Background(), "chan-1", "sauti-ivr-fallback", "s", 1); err != nil {
		t.Fatal(err)
	}
	if got := f.Continued("chan-1"); got != "sauti-ivr-fallback,s,1" {
		t.Fatalf("continued to %q", got)
	}
	if e := next(t, ch, EventStasisEnd); e.ChannelID != "chan-1" {
		t.Fatalf("stasis end = %+v", e)
	}
	if err := c.Continue(context.Background(), "chan-1", "bad context!", "s", 1); err == nil {
		t.Fatal("an unsafe context name was sent")
	}
}

// I4: readiness sees whether the event stream is up.
func TestClientReportsEventStream(t *testing.T) {
	f, c := newPair(t)
	if c.EventsConnected() {
		t.Fatal("connected before streaming")
	}
	_, cancel := stream(t, c)
	<-f.Connected()
	waitUntil := func(want bool) bool {
		deadline := time.Now().Add(3 * time.Second)
		for c.EventsConnected() != want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		return c.EventsConnected() == want
	}
	if !waitUntil(true) {
		t.Fatal("not connected while streaming")
	}
	f.DropStreams()
	cancel()
	if !waitUntil(false) {
		t.Fatal("still connected after the stream ended")
	}
}
