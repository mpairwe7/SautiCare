package ivr

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"sautihealth.com/sauti/internal/platform/ari"
)

// stasisARI models Asterisk's rule that continue and play work only on a
// channel still in the Stasis application (409 otherwise), while hang-up
// works on any live channel. With stale set, ListChannels keeps reporting a
// channel that has already left (a list read just before a hand-over).
type stasisARI struct {
	mu       sync.Mutex
	inStasis map[string]bool
	stale    []ari.Channel
	hangups  []string
	conts    []string
}

func notInStasis(ch string) error {
	return fmt.Errorf("ari: channel %s: status 409: %w", ch, ari.ErrNotInStasis)
}

func (s *stasisARI) Answer(context.Context, string) error { return nil }
func (s *stasisARI) Play(_ context.Context, ch, _ string, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inStasis[ch] {
		return notInStasis(ch)
	}
	return nil
}
func (s *stasisARI) StopPlayback(context.Context, string) error         { return nil }
func (s *stasisARI) SetReference(context.Context, string, string) error { return nil }
func (s *stasisARI) Continue(_ context.Context, ch, _, _ string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.inStasis[ch] {
		return notInStasis(ch)
	}
	delete(s.inStasis, ch)
	s.conts = append(s.conts, ch)
	return nil
}
func (s *stasisARI) Hangup(_ context.Context, ch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hangups = append(s.hangups, ch)
	return nil
}
func (s *stasisARI) ListChannels(context.Context) ([]ari.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stale != nil {
		return slices.Clone(s.stale), nil
	}
	var out []ari.Channel
	for ch := range s.inStasis {
		out = append(out, ari.Channel{ID: ch, App: "sauti-ivr"})
	}
	return out, nil
}

func (s *stasisARI) outcome() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.conts), slices.Clone(s.hangups)
}

// Review R1: two adoptions of one orphan (a stream that reconnects twice in
// a few seconds; a list read before the first hand-over) put the caller
// through once, and the second never hangs up the caller the first put
// through.
func TestOverlappingAdoptsHandOverOnce(t *testing.T) {
	a := &stasisARI{inStasis: map[string]bool{"ch9": true}, stale: []ari.Channel{{ID: "ch9", App: "sauti-ivr"}}}
	c := New(a, &scripted{prompts: prompts()}, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _ = c.Adopt(context.Background()) })
	}
	wg.Wait()
	_ = c.Adopt(context.Background()) // and a third, later, on the same stale list
	if conts, hangups := a.outcome(); !slices.Equal(conts, []string{"ch9"}) || len(hangups) != 0 {
		t.Fatalf("continues %v hangups %v", conts, hangups)
	}
}

// Review R1: a call another instance (or the dialplan) has already taken out
// of the application is not hung up by this instance's worker, whose prompt
// and hand-over now fail with "not in Stasis".
func TestACallThatLeftTheApplicationIsNotHungUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		a := &stasisARI{inStasis: map[string]bool{"ch1": true}}
		opt := Options{NoInput: 15 * time.Second, PromptMax: 30 * time.Second}
		first := New(a, &scripted{prompts: prompts()}, slog.New(slog.DiscardHandler), opt)
		second := New(a, &scripted{prompts: prompts()}, slog.New(slog.DiscardHandler), opt)
		first.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		if err := second.Adopt(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Minute) // the first instance's timers run out with no events
		synctest.Wait()
		if conts, hangups := a.outcome(); !slices.Equal(conts, []string{"ch1"}) || len(hangups) != 0 {
			t.Fatalf("continues %v hangups %v", conts, hangups)
		}
	})
}
