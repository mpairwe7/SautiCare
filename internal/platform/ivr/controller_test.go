package ivr

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/ari"
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
	channels     []ari.Channel // what ListChannels reports
	seq          []string      // reference, hand-over escalation and continue requests, in order
	failRef      bool
	failHangup   bool
}

func (f *fakeARI) SetReference(_ context.Context, ch, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRef {
		return errors.New("asterisk: variable refused")
	}
	f.seq = append(f.seq, "ref "+ch+" "+ref)
	return nil
}

func (f *fakeARI) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq = append(f.seq, s)
}

func (f *fakeARI) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seq)
}

// ListChannels reports channels only when the test set them; otherwise it
// fails, as an unreachable Asterisk does (a silent call is then hung up).
func (f *fakeARI) ListChannels(context.Context) ([]ari.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.channels == nil {
		return nil, errors.New("asterisk: no channel list in this test")
	}
	return slices.Clone(f.channels), nil
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
	f.seq = append(f.seq, "continue "+ch)
	f.continues = append(f.continues, ch+" "+dialContext+","+ext+","+string(rune('0'+prio)))
	return nil
}

func (f *fakeARI) Hangup(_ context.Context, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failHangup {
		return errors.New("asterisk: hangup failed")
	}
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
	invalid   map[string]bool // inputs that re-prompt the same question with a notice
	handovers []string        // HandOver reasons
	failHand  int             // HandOver fails this many times first
	log       *fakeARI        // where HandOver notes itself among the ARI requests
}

func (s *scripted) HandOver(_ context.Context, c intake.IVRCall, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handovers = append(s.handovers, c.CallID+" "+reason)
	if s.log != nil {
		s.log.note("handover " + c.CallID)
	}
	if s.failHand > 0 {
		s.failHand--
		return errors.New("store down")
	}
	return nil
}

func (s *scripted) handedOver() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.handovers)
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
	return intake.IVRTurn{Call: intake.IVRCall{CallID: callID, IncidentID: uuid.NewV7(), Reference: "1234567894", HasPhone: !phone.IsZero(),
		Shown: s.prompts[0].Kind}, Prompt: s.prompts[0]}, nil
}

func (s *scripted) AnswerIVR(_ context.Context, c intake.IVRCall, input string) (intake.IVRTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input == s.failOn {
		return intake.IVRTurn{}, errors.New("store down")
	}
	if s.invalid[input] {
		pr := s.prompts[min(len(s.inputs), len(s.prompts)-1)]
		pr.Notice = "invalid"
		return intake.IVRTurn{Call: c, Prompt: pr}, nil
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

// Silence from the very first prompt re-plays it, then the call ends: a
// pocket dial (the abandonment sweeper and resume take it from there).
func TestSilenceFromTheFirstPromptHangsUp(t *testing.T) {
	s := &scripted{prompts: prompts()}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: 20 * time.Millisecond, MaxReprompts: 2})
	a.ctrl = c
	c.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: ""})
	waitFor(t, "the hang-up", func() bool { return a.count(&a.hangups) == 1 })
	if n := a.playCount(); n != 3 || a.count(&a.continues) != 0 || len(s.handedOver()) != 0 {
		t.Fatalf("plays %d, want 3 (prompt + 2 re-prompts) then hang-up; continues %v", n, a.continues)
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

// A prompt whose PlaybackFinished is lost (an event dropped between Asterisk
// and the controller) is still followed by a re-prompt: the backstop armed
// when the prompt is requested fires at PromptMax + NoInput.
func TestALostEndOfPromptStillRepromptsWithinTheBackstop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		a := &fakeARI{}
		c := New(a, &scripted{prompts: prompts()}, slog.New(slog.DiscardHandler),
			Options{NoInput: 15 * time.Second, MaxReprompts: 2, PromptMax: 30 * time.Second})
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		pb, first := a.lastPlay()
		c.Handle(ctx, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb}) // its PlaybackFinished never comes
		time.Sleep(45*time.Second - time.Millisecond)
		synctest.Wait()
		if n := a.playCount(); n != 1 {
			t.Fatalf("plays %d before the backstop", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if _, second := a.lastPlay(); a.playCount() != 2 || !slices.Equal(second, first) {
			t.Fatalf("plays %v, want the first prompt again", a.plays)
		}
	})
}

// After a restart or a dropped event stream, a call parked in the IVR's
// application with no worker (its events are gone) goes to a coordinator.
// Its StasisStart, if still in flight, opens nothing: the call has left.
func TestReconnectHandsOverOrphanedChannels(t *testing.T) {
	c, a, _ := newController(t, &scripted{prompts: prompts()})
	a.channels = []ari.Channel{{ID: "ch9", App: "sauti-ivr"}, {ID: "out-1", App: "sauti-alerts"}}
	if err := c.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.continues, []string{"ch9 sauti-ivr-fallback,s,1"}) {
		t.Fatalf("continues %v", a.continues)
	}
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch9", CallerNumber: "+256772123456"})
	if a.count(&a.answers) != 0 {
		t.Fatal("served a call already handed over")
	}
}

func TestReconnectLeavesOwnedChannelsAlone(t *testing.T) {
	c, a, _ := newController(t, &scripted{prompts: prompts()})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	a.channels = []ari.Channel{{ID: "ch1", App: "sauti-ivr"}}
	if err := c.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.count(&a.continues) != 0 || a.count(&a.hangups) != 0 {
		t.Fatalf("continues %v hangups %v", a.continues, a.hangups)
	}
}

// ivr-controller stopping mid-call hands the caller to a coordinator, once,
// instead of leaving them in silence.
func TestShutdownHandsOverLiveCalls(t *testing.T) {
	c, a, _ := newController(t, &scripted{prompts: prompts()})
	ctx, cancel := context.WithCancel(context.Background())
	c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	settle(t, c)
	started(t, c, a, "ch1")
	cancel()
	waitFor(t, "the hand-over", func() bool { return a.count(&a.continues) == 1 })
	time.Sleep(20 * time.Millisecond)
	if !slices.Equal(a.continues, []string{"ch1 sauti-ivr-fallback,s,1"}) || a.count(&a.hangups) != 0 {
		t.Fatalf("continues %v hangups %v", a.continues, a.hangups)
	}
}

// Three wrong keys on one question reach a coordinator (ADR-0029 rule 2): a
// caller who cannot find the right key is never looped for ever.
func TestThreeNoMatchesHandOver(t *testing.T) {
	s := &scripted{prompts: prompts(), invalid: map[string]bool{"0": true}}
	c, a, _ := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	for i := range 3 {
		if a.count(&a.continues) != 0 {
			t.Fatalf("handed over after %d wrong keys", i)
		}
		started(t, c, a, "ch1")
		send(t, c, key("ch1", "0"))
	}
	if !slices.Equal(a.continues, []string{"ch1 sauti-ivr-fallback,s,1"}) || a.playCount() != 3 ||
		!slices.Equal(s.handedOver(), []string{"ch1 voice_handed_over"}) {
		t.Fatalf("continues %v plays %d handovers %v", a.continues, a.playCount(), s.handovers)
	}
}

// A valid answer resets the count: two wrong keys, a right one, then two
// wrong keys on the next question do not hand over.
func TestNoMatchCountResetsOnAValidAnswer(t *testing.T) {
	choice := []intake.MenuOption{{Digit: 1, Value: "yes"}, {Digit: 2, Value: "no"}}
	s := &scripted{prompts: []intake.Prompt{prompts()[0], {Kind: intake.KindDanger, Lang: "en", Options: choice}, prompts()[2]},
		invalid: map[string]bool{"0": true}}
	c, a, _ := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	for _, d := range []string{"0", "0", "2", "0", "0"} {
		started(t, c, a, "ch1")
		send(t, c, key("ch1", d))
	}
	if a.count(&a.continues) != 0 {
		t.Fatalf("continues %v", a.continues)
	}
}

// Silence after the caller has answered a screen is not a pocket dial: after
// the re-prompts the caller reaches a coordinator, not a hang-up.
func TestSilenceAfterAnAnswerHandsOver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s := &scripted{prompts: prompts()}
		a := &fakeARI{}
		c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: 15 * time.Second, MaxReprompts: 2})
		finish := func() {
			pb, _ := a.lastPlay()
			c.Handle(ctx, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb})
			c.Handle(ctx, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb})
			synctest.Wait()
		}
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		finish()
		c.Handle(ctx, key("ch1", "1"))
		synctest.Wait()
		for range 3 { // the question and its two re-prompts, each met by silence
			finish()
			time.Sleep(15 * time.Second)
			synctest.Wait()
		}
		if !slices.Equal(a.continues, []string{"ch1 sauti-ivr-fallback,s,1"}) || a.count(&a.hangups) != 0 || a.playCount() != 4 {
			t.Fatalf("continues %v hangups %v plays %d", a.continues, a.hangups, a.playCount())
		}
	})
}

// The escalation is recorded first, so the coordinator line rings even when
// the transfer itself fails; the caller is then hung up, not left in silence.
func TestHandOverRecordsAnEscalationEvenWhenContinueFails(t *testing.T) {
	s := &scripted{prompts: prompts(), failOn: "1"}
	a := &fakeARI{failContinue: true}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	if !slices.Equal(s.handedOver(), []string{"ch1 voice_handed_over"}) || a.count(&a.hangups) != 1 {
		t.Fatalf("handovers %v hangups %v", s.handovers, a.hangups)
	}
}

// The coordinator taking a handed-over call sees "SAUTI" and the reference:
// the controller sets only the reference, and the fallback dialplan masks
// the caller ID itself, so no failed request can show the caller's number
// (rule 5; ADR-0029 rule 6).
func TestHandOverShowsSautiAndTheReference(t *testing.T) {
	s := &scripted{prompts: prompts(), failOn: "1"}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	want := []string{"ref ch1 123 456 7894", "continue ch1"}
	if !slices.Equal(a.requests(), want) {
		t.Fatalf("requests %v, want %v", a.requests(), want)
	}
}

// A reference Asterisk will not set does not hold the caller back: the
// dialplan then shows "SAUTI" alone.
func TestHandOverGoesAheadWhenTheReferenceCannotBeSet(t *testing.T) {
	s := &scripted{prompts: prompts(), failOn: "1"}
	a := &fakeARI{failRef: true}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	started(t, c, a, "ch1")
	send(t, c, key("ch1", "1"))
	if !slices.Equal(a.continues, []string{"ch1 sauti-ivr-fallback,s,1"}) || a.count(&a.hangups) != 0 {
		t.Fatalf("continues %v hangups %v", a.continues, a.hangups)
	}
}

// A hand-over escalation the store refuses is retried in the background
// every 5 s (up to 10 minutes) with the same call, so its key keeps it to
// one event; the caller is put through at once.
func TestAFailedHandOverEscalationIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &scripted{prompts: prompts(), failOn: "1", failHand: 2}
		a := &fakeARI{}
		c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		pb, _ := a.lastPlay()
		c.Handle(ctx, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb})
		c.Handle(ctx, key("ch1", "1"))
		synctest.Wait()
		if a.count(&a.continues) != 1 || len(s.handedOver()) != 1 {
			t.Fatalf("continues %v handovers %v: the call waited for the escalation", a.continues, s.handovers)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := s.handedOver(); !slices.Equal(got, []string{"ch1 voice_handed_over", "ch1 voice_handed_over", "ch1 voice_handed_over"}) {
			t.Fatalf("handovers %v, want three of the same call", got)
		}
		time.Sleep(15 * time.Minute)
		synctest.Wait()
		if n := len(s.handedOver()); n != 3 {
			t.Fatalf("retried after success: %d", n)
		}
	})
}

// Task 1.2 amendment: a call adopted after a restart is escalated as a live
// hand-over is, under its call ID (the channel), before the transfer.
func TestAdoptEscalatesBeforeItHandsOver(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	s.log = a
	a.channels = []ari.Channel{{ID: "ch9", App: "sauti-ivr"}}
	if err := c.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := a.requests(); !slices.Equal(got, []string{"handover ch9", "continue ch9"}) || !slices.Equal(s.handedOver(), []string{"ch9 voice_handed_over"}) {
		t.Fatalf("requests %v handovers %v", got, s.handovers)
	}
}

// The escalation stands when the adopted call cannot be transferred: the
// coordinator line still rings, and the call is hung up, not left silent.
func TestAdoptEscalatesWhenTheFallbackFails(t *testing.T) {
	s := &scripted{prompts: prompts()}
	a := &fakeARI{failContinue: true}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	a.channels = []ari.Channel{{ID: "ch9", App: "sauti-ivr"}}
	if err := c.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.handedOver(), []string{"ch9 voice_handed_over"}) || !slices.Equal(a.hangups, []string{"ch9"}) {
		t.Fatalf("handovers %v hangups %v", s.handovers, a.hangups)
	}
}

// A question whose recording failed to play left the caller in silence: a
// coordinator takes the call (F-03), instead of re-prompting into silence.
func TestAFailedQuestionPromptHandsOver(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	pb := started(t, c, a, "ch1")
	send(t, c, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb, PlaybackState: ari.PlaybackFailed})
	if !slices.Equal(a.continues, []string{"ch1 sauti-ivr-fallback,s,1"}) || !slices.Equal(s.handedOver(), []string{"ch1 voice_handed_over"}) {
		t.Fatalf("continues %v handovers %v", a.continues, s.handovers)
	}
}

// Review R3: a closing message longer than PromptMax (the reference read
// digit by digit, then its message) is not cut off by the backstop: each
// recording of the playback re-arms it as it starts (PlaybackContinuing).
func TestALongClosingPromptIsNotCutOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		a := &fakeARI{}
		c := New(a, &scripted{prompts: prompts()[2:]}, slog.New(slog.DiscardHandler),
			Options{NoInput: 15 * time.Second, PromptMax: 30 * time.Second})
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		pb, _ := a.lastPlay()
		c.Handle(ctx, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb})
		for range 3 { // each recording plays 40 s
			time.Sleep(40 * time.Second)
			c.Handle(ctx, Event{Type: EventPlaybackContinuing, ChannelID: "ch1", PlaybackID: pb})
			synctest.Wait()
		}
		if a.count(&a.hangups) != 0 {
			t.Fatal("hung up while the closing message played")
		}
		c.Handle(ctx, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb})
		synctest.Wait()
		if a.count(&a.hangups) != 1 {
			t.Fatalf("hangups %v after the closing message", a.hangups)
		}
	})
}

// CodeRabbit (PR #13, C3): a hand-over escalation still refused after 10
// minutes is lost from memory; it is counted (the metric pages, with the
// runbook's recovery step) and stops retrying.
func TestALostHandOverEscalationIsCounted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &scripted{prompts: prompts(), failOn: "1", failHand: 1 << 20}
		a := &fakeARI{}
		var lost atomic.Int32
		c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour, OnEscalationLost: func() { lost.Add(1) }})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		pb, _ := a.lastPlay()
		c.Handle(ctx, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb})
		c.Handle(ctx, key("ch1", "1"))
		time.Sleep(9 * time.Minute)
		synctest.Wait()
		if lost.Load() != 0 {
			t.Fatalf("counted lost after 9 minutes")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		n := len(s.handedOver())
		time.Sleep(time.Hour)
		synctest.Wait()
		if lost.Load() != 1 || len(s.handedOver()) != n {
			t.Fatalf("lost %d, retries went on: %d then %d", lost.Load(), n, len(s.handedOver()))
		}
	})
}

// PR #14 review: a call whose intake fails after its session opened is
// escalated under its call ID (the channel), so HandOver finds the incident
// the failed start left behind.
func TestAStartFailureEscalatesUnderTheCallID(t *testing.T) {
	s := &scripted{prompts: prompts(), failStart: true}
	c := New(&fakeARI{}, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	send(t, c, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	if got := s.handedOver(); !slices.Equal(got, []string{"ch1 voice_handed_over"}) {
		t.Fatalf("handovers %v", got)
	}
}

// PR #14 review: an orphan whose transfer and hang-up both failed (Asterisk
// briefly unreachable) is still in the application; the next adoption tries
// it again rather than skipping it for ever.
func TestAdoptRetriesAnOrphanItCouldNotMove(t *testing.T) {
	s := &scripted{prompts: prompts()}
	a := &fakeARI{failContinue: true, failHangup: true, channels: []ari.Channel{{ID: "ch9", App: "sauti-ivr"}}}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour})
	if err := c.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.failContinue, a.failHangup = false, false
	a.mu.Unlock()
	if err := c.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.continues, []string{"ch9 sauti-ivr-fallback,s,1"}) {
		t.Fatalf("continues %v", a.continues)
	}
}

// PR #14 review: a hand-over escalation the store refuses while the
// controller stops is not left to a retry the exit would kill: one last try
// is made at once, and if it fails the loss is logged and counted before
// the worker set lets the process exit.
func TestAnEscalationRefusedAtShutdownIsCountedBeforeExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &scripted{prompts: prompts(), failHand: 1 << 20}
		a := &fakeARI{}
		var lost atomic.Int32
		c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour, OnEscalationLost: func() { lost.Add(1) }})
		ctx, cancel := context.WithCancel(t.Context())
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		cancel() // the controller stops with the call live
		c.Wait()
		if lost.Load() != 1 || len(s.handedOver()) != 2 {
			t.Fatalf("lost %d, attempts %d: want the shutdown hand-over, one last try, then the loss", lost.Load(), len(s.handedOver()))
		}
	})
}

// PR #14 review: when the shutdown deadline passes with a hand-over
// escalation still being retried, FlushLost reports it lost at once (logged
// and counted) so the exit never drops it silently; the retry's own end
// does not count it twice.
func TestFlushLostReportsEveryPendingRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &scripted{prompts: prompts(), failOn: "1", failHand: 1 << 20}
		a := &fakeARI{}
		var lost atomic.Int32
		c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: time.Hour, OnEscalationLost: func() { lost.Add(1) }})
		ctx, cancel := context.WithCancel(t.Context())
		c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
		synctest.Wait()
		pb, _ := a.lastPlay()
		c.Handle(ctx, Event{Type: EventPlaybackStarted, ChannelID: "ch1", PlaybackID: pb})
		c.Handle(ctx, key("ch1", "1")) // intake fails: the hand-over's escalation is refused and retried
		synctest.Wait()
		c.FlushLost() // the shutdown deadline passed
		if n := lost.Load(); n != 1 {
			t.Fatalf("lost %d at the deadline, want 1", n)
		}
		cancel()
		c.Wait()
		if n := lost.Load(); n != 1 {
			t.Fatalf("lost %d after the retry ended, want it counted once", n)
		}
	})
}
