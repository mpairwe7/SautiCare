package ivr

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/logging"
)

// fakeARI records what the controller asks of Asterisk. With ctrl set it
// plays like Asterisk does by default: every prompt starts and finishes at
// once (PlaybackStarted, PlaybackFinished back to the controller).
type fakeARI struct {
	mu           sync.Mutex
	plays        [][]string
	ids          []string
	answers      []string
	hangups      []string
	stops        []string
	continues    []string
	failAnswer   bool
	failContinue bool
	failPlay     func(sounds []string) bool
	ctrl         *Controller
}

func (f *fakeARI) Answer(_ context.Context, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, ch)
	if f.failAnswer {
		return errors.New("asterisk: answer failed")
	}
	return nil
}

func (f *fakeARI) Play(_ context.Context, ch, pb string, sounds []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plays, f.ids = append(f.plays, sounds), append(f.ids, pb)
	if f.failPlay != nil && f.failPlay(sounds) {
		return errors.New("asterisk: play failed")
	}
	if c := f.ctrl; c != nil {
		go func() {
			c.Handle(context.Background(), Event{Type: EventPlaybackStarted, ChannelID: ch, PlaybackID: pb})
			c.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: ch, PlaybackID: pb})
		}()
	}
	return nil
}

func (f *fakeARI) StopPlayback(_ context.Context, pb string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, pb)
	return nil
}

func (f *fakeARI) Continue(_ context.Context, ch, dialContext, ext string, prio int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failContinue {
		return errors.New("asterisk: continue failed")
	}
	f.continues = append(f.continues, ch+" "+dialContext+","+ext+","+string(rune('0'+prio)))
	return nil
}

func (f *fakeARI) Hangup(_ context.Context, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hangups = append(f.hangups, ch)
	return nil
}

func (f *fakeARI) lastPlay() (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ids[len(f.ids)-1], f.plays[len(f.plays)-1]
}

func (f *fakeARI) count(what *[]string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(*what)
}

func (f *fakeARI) playCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.plays)
}

// scripted is an intake stand-in: each answer moves to the next prompt.
// A call whose ID is in block waits in StartIVR until released or its
// deadline.
type scripted struct {
	mu        sync.Mutex
	prompts   []intake.Prompt
	inputs    []string
	phone     pii.Phone
	failOn    string
	failStart bool
	block     map[string]chan struct{}
}

func (s *scripted) StartIVR(ctx context.Context, callID string, phone pii.Phone) (intake.IVRTurn, error) {
	s.mu.Lock()
	wait := s.block[callID]
	s.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return intake.IVRTurn{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStart {
		return intake.IVRTurn{}, errors.New("store down")
	}
	s.phone = phone
	return intake.IVRTurn{Call: intake.IVRCall{CallID: callID, IncidentID: uuid.NewV7(), HasPhone: !phone.IsZero(), Shown: s.prompts[0].Kind},
		Prompt: s.prompts[0]}, nil
}

func (s *scripted) AnswerIVR(_ context.Context, c intake.IVRCall, input string) (intake.IVRTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input == s.failOn {
		return intake.IVRTurn{}, errors.New("store down")
	}
	s.inputs = append(s.inputs, input)
	next := s.prompts[min(len(s.inputs), len(s.prompts)-1)]
	c.Shown = next.Kind
	return intake.IVRTurn{Call: c, Prompt: next}, nil
}

func (s *scripted) answered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inputs)
}

func prompts() []intake.Prompt {
	return []intake.Prompt{
		{Kind: intake.KindLanguage, Lang: "*", Options: []intake.MenuOption{{Digit: 1, Value: "en"}, {Digit: 2, Value: "lg"}}},
		{Kind: intake.KindCallback, Lang: "en", Options: []intake.MenuOption{{Digit: 1, Value: "same_number"}}},
		{Kind: intake.KindComplete, Lang: "en", End: true, Key: "trauma", Reference: "123 456 7894"},
	}
}

func newController(t *testing.T, s *scripted) (*Controller, *fakeARI, *bytes.Buffer) {
	t.Helper()
	a := &fakeARI{}
	var logs bytes.Buffer
	c := New(a, s, slog.New(logging.NewHandler(&syncWriter{w: &logs}, slog.LevelDebug)), Options{NoInput: time.Hour, MaxReprompts: 2})
	return c, a, &logs
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// settle waits until the controller has handled every event given to it.
func settle(t *testing.T, c *Controller) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for c.pending.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("controller did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// send hands events to the controller and waits until they are handled.
func send(t *testing.T, c *Controller, evs ...Event) {
	t.Helper()
	for _, e := range evs {
		c.Handle(context.Background(), e)
	}
	settle(t, c)
}

// started tells the controller the last prompt began playing.
func started(t *testing.T, c *Controller, a *fakeARI, ch string) string {
	t.Helper()
	pb, _ := a.lastPlay()
	send(t, c, Event{Type: EventPlaybackStarted, ChannelID: ch, PlaybackID: pb})
	return pb
}

func key(ch, d string) Event { return Event{Type: EventDTMF, ChannelID: ch, Digit: d} }

func TestControllerRunsACall(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, logs := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	if len(a.answers) != 1 || s.phone.E164() != "+256772123456" {
		t.Fatalf("answers %v phone %v", a.answers, s.phone)
	}
	if _, snd := a.lastPlay(); snd[0] != "sauti/language" {
		t.Fatalf("first play %v", snd)
	}
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	// Callback screen: digits accumulate until '#'.
	started(t, c, a, "ch1")
	for _, d := range "0772999888" {
		send(t, c, key("ch1", string(d)))
	}
	if got := s.answered(); len(got) != 1 {
		t.Fatalf("answered before '#': %v", got)
	}
	send(t, c, key("ch1", "#"))
	if got := s.answered(); len(got) != 2 || got[1] != "0772999888" {
		t.Fatalf("inputs = %v", got)
	}
	pb, snd := a.lastPlay()
	if !strings.Contains(strings.Join(snd, " "), "sauti/digits/7") || a.count(&a.hangups) != 0 {
		t.Fatalf("closing play %v, hangups %v", snd, a.hangups)
	}
	send(t, c, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb}, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb})
	if a.count(&a.hangups) != 1 {
		t.Fatal("not hung up after the closing message")
	}
	if strings.Contains(logs.String(), "772123456") || strings.Contains(logs.String(), "772999888") {
		t.Fatalf("log carries a number:\n%s", logs.String())
	}
}

// "Press 1 for this number" is a single key when the caller ID is known.
func TestControllerSameNumberIsOneKey(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	if got := s.answered(); len(got) != 2 || got[1] != "1" {
		t.Fatalf("inputs = %v", got)
	}
}

// Silence after a prompt re-plays it, then the call ends (the abandonment
// sweeper and resume take it from there).
func TestControllerNoInputRepromptsThenHangsUp(t *testing.T) {
	s := &scripted{prompts: prompts()}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: 20 * time.Millisecond, MaxReprompts: 2})
	a.ctrl = c
	c.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: ""})
	waitFor(t, "the hang-up", func() bool { return a.count(&a.hangups) == 1 })
	if n := a.playCount(); n != 3 {
		t.Fatalf("plays %d, want 3 (prompt + 2 re-prompts) then hang-up", n)
	}
}

// I2(a): ARI queues playbacks. A key pressed during a prompt stops it, and
// a second press of the same key before the next prompt has started (a
// double press, or a key meant for the old menu) is not taken as the answer
// to the next screen.
func TestControllerKeyStopsThePromptAndADoublePressAnswersOnce(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	first := started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"), key("ch1", "1"))
	if got := s.answered(); len(got) != 1 {
		t.Fatalf("answers %v: the second press answered the next screen", got)
	}
	if a.count(&a.stops) != 1 || a.stops[0] != first {
		t.Fatalf("stops %v, want the playing prompt %s", a.stops, first)
	}
	// Once the callback prompt has started, a key answers it.
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	if got := s.answered(); len(got) != 2 {
		t.Fatalf("answers %v", got)
	}
}

// I2(b): the no-input timer runs from the end of the prompt, not from the
// request to play it: a 30 s prompt is not re-played while it plays.
func TestControllerNoInputWaitsForThePromptToFinish(t *testing.T) {
	s := &scripted{prompts: prompts()}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: 20 * time.Millisecond, MaxReprompts: 2})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: ""})
	pb := started(t, c, a, "ch1")
	time.Sleep(150 * time.Millisecond) // the long prompt is still playing
	settle(t, c)
	if n := a.playCount(); n != 1 || a.count(&a.hangups) != 0 {
		t.Fatalf("plays %d, hangups %d while the first prompt plays", n, a.count(&a.hangups))
	}
	send(t, c, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb})
	waitFor(t, "the re-prompt", func() bool { return a.playCount() == 2 })
}

// I3: one call blocked in intake does not hold up another call's prompt.
func TestControllerSlowCallDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	s := &scripted{prompts: prompts(), block: map[string]chan struct{}{"slow": release}}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour, OpTimeout: 10 * time.Second})
	ctx := context.Background()
	c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "slow", CallerNumber: "+256772123456"})
	waitFor(t, "the slow call to be answered", func() bool { return a.count(&a.answers) == 1 })
	c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "fast", CallerNumber: "+256772000111"})
	waitFor(t, "the fast call's first prompt", func() bool { return a.playCount() == 1 })
	if pb, _ := a.lastPlay(); !strings.HasPrefix(pb, "fast-") {
		t.Fatalf("first prompt %s", pb)
	}
	close(release)
	waitFor(t, "the slow call's first prompt", func() bool { return a.playCount() == 2 })
}

// I3/I4: every intake operation has its own deadline. A call whose intake
// does not answer in time is handed to a coordinator, not left in silence.
func TestControllerIntakeDeadlineHandsOver(t *testing.T) {
	s := &scripted{prompts: prompts(), block: map[string]chan struct{}{"stuck": make(chan struct{})}}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour, OpTimeout: 50 * time.Millisecond})
	c.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "stuck", CallerNumber: "+256772123456"})
	waitFor(t, "the hand-over", func() bool { return a.count(&a.continues) == 1 })
	if a.continues[0] != "stuck sauti-ivr-fallback,s,1" || a.count(&a.hangups) != 0 {
		t.Fatalf("continues %v hangups %v", a.continues, a.hangups)
	}
}

// I4: if intake fails mid-call, the caller is handed to the dialplan's
// fallback (a recorded message, then a coordinator), never hung up on; the
// error is logged without the number. Every answer so far is persisted.
func TestControllerIntakeErrorHandsOverToACoordinator(t *testing.T) {
	s := &scripted{prompts: prompts(), failOn: "1"}
	c, a, logs := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	if a.count(&a.continues) != 1 || a.continues[0] != "ch1 sauti-ivr-fallback,s,1" || a.count(&a.hangups) != 0 {
		t.Fatalf("continues %v hangups %v", a.continues, a.hangups)
	}
	if !strings.Contains(logs.String(), "store down") || strings.Contains(logs.String(), "772123456") {
		t.Fatalf("logs:\n%s", logs.String())
	}
	send(t, c, Event{Type: EventStasisEnd, ChannelID: "ch1"}, key("ch1", "1"))
	if len(s.answered()) != 0 {
		t.Fatal("a call handed over still answered")
	}
}

// I4: a call that cannot be answered or opened goes to the coordinator too.
func TestControllerAnswerOrStartFailureHandsOver(t *testing.T) {
	for _, tc := range []struct {
		name string
		ari  *fakeARI
		s    *scripted
	}{
		{"answer", &fakeARI{failAnswer: true}, &scripted{prompts: prompts()}},
		{"start", &fakeARI{}, &scripted{prompts: prompts(), failStart: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.ari, tc.s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
			send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
			if tc.ari.count(&tc.ari.continues) != 1 || tc.ari.playCount() != 0 {
				t.Fatalf("continues %v plays %d", tc.ari.continues, tc.ari.playCount())
			}
		})
	}
}

// I4: if the hand-over itself fails, the call is hung up (the caller can
// redial) rather than left in silence.
func TestControllerFailedHandOverHangsUp(t *testing.T) {
	a := &fakeARI{failContinue: true}
	c := New(a, &scripted{prompts: prompts(), failStart: true}, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: ""})
	if a.count(&a.hangups) != 1 {
		t.Fatalf("hangups %v", a.hangups)
	}
}

// I4: a closing prompt that cannot be played would never send
// PlaybackFinished: the call is hung up at once instead of waiting forever.
// A question that cannot be played hands the caller to a coordinator.
func TestControllerFailedPlay(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	a.failPlay = func(snd []string) bool { return snd[0] == "sauti/en/complete" }
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	if a.count(&a.hangups) != 1 {
		t.Fatalf("closing prompt failed: hangups %v", a.hangups)
	}

	s2 := &scripted{prompts: prompts()}
	c2, a2, _ := newController(t, s2)
	a2.failPlay = func([]string) bool { return true }
	send(t, c2, Event{Type: EventStasisStart, ChannelID: "ch2", CallerNumber: "+256772123456"})
	if a2.count(&a2.continues) != 1 || a2.count(&a2.hangups) != 0 {
		t.Fatalf("first prompt failed: continues %v hangups %v", a2.continues, a2.hangups)
	}
}

// A repeated StasisStart for a call in progress opens nothing new.
func TestControllerRepeatedStartIsIgnored(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	start := Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"}
	send(t, c, start, start)
	if a.count(&a.answers) != 1 || a.playCount() != 1 {
		t.Fatalf("answers %d plays %d", a.count(&a.answers), a.playCount())
	}
}

func TestControllerIgnoresUnknownChannels(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	send(t, c, key("nobody", "1"))
	if a.playCount() != 0 || len(s.answered()) != 0 {
		t.Fatal("acted on a channel that never started")
	}
}

// Sound IDs are plain path segments: no wildcard or empty language part.
func TestSoundIDsAreClean(t *testing.T) {
	for _, s := range requiredSounds(t) {
		if strings.Contains(s, "*") || strings.Contains(s, "//") || strings.HasSuffix(s, "/") {
			t.Errorf("sound ID %q", s)
		}
	}
}
