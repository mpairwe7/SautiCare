package ivr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/platform/sim"
)

// I2 over the real client and a fake Asterisk that queues playbacks: the
// caller presses 1 twice while the long language prompt plays. One answer is
// recorded; the callback prompt is not answered by the second press; and the
// callback prompt, which plays on, is not re-played until it has finished.
func TestControllerOverAsteriskQueue(t *testing.T) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	pw := hex.EncodeToString(b) // random per run: no password literal
	f := sim.NewFakeARI("sauti", pw)
	t.Cleanup(f.Close)
	f.SetPlaybackDuration(-1) // long prompts: they play until stopped or finished
	client, err := ari.New(ari.Config{BaseURL: f.URL(), User: "sauti", Password: pw, App: "sauti-ivr"})
	if err != nil {
		t.Fatal(err)
	}
	s := &scripted{prompts: prompts()}
	c := New(client, s, slog.New(slog.DiscardHandler), Options{NoInput: 50 * time.Millisecond, MaxReprompts: 2})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx, client) }()
	<-f.Connected()

	f.StartCall("chan-1", "+256772123456")
	waitFor(t, "the language prompt", func() bool { return f.Playing("chan-1") != "" })
	first := f.Playing("chan-1")
	f.Press("chan-1", "11")
	waitFor(t, "the callback prompt", func() bool { p := f.Playing("chan-1"); return p != "" && p != first })
	time.Sleep(300 * time.Millisecond) // six no-input periods while the prompt plays
	settle(t, c)
	if got := s.answered(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("answers %v, want one", got)
	}
	if n := len(f.Plays("chan-1")); n != 2 {
		t.Fatalf("plays %d while the callback prompt plays, want 2", n)
	}
	f.FinishPlayback(f.Playing("chan-1"))
	waitFor(t, "the re-prompt after silence", func() bool { return len(f.Plays("chan-1")) == 3 })
}
