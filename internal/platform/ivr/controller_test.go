package ivr

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/logging"
)

type fakeARI struct {
	mu      sync.Mutex
	plays   [][]string
	ids     []string
	answers []string
	hangups []string
}

func (f *fakeARI) Answer(_ context.Context, ch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, ch)
	return nil
}

func (f *fakeARI) Play(_ context.Context, ch, pb string, sounds []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plays, f.ids = append(f.plays, sounds), append(f.ids, pb)
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

// scripted is an intake stand-in: each answer moves to the next prompt.
type scripted struct {
	mu      sync.Mutex
	prompts []intake.Prompt
	inputs  []string
	phone   pii.Phone
	failOn  string
}

func (s *scripted) StartIVR(_ context.Context, callID string, phone pii.Phone) (intake.IVRTurn, error) {
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
	c := New(a, s, slog.New(logging.NewHandler(&logs, slog.LevelDebug)), Options{NoInput: time.Hour, MaxReprompts: 2})
	return c, a, &logs
}

func TestControllerRunsACall(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, logs := newController(t, s)
	ctx := context.Background()
	c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	if len(a.answers) != 1 || s.phone.E164() != "+256772123456" {
		t.Fatalf("answers %v phone %v", a.answers, s.phone)
	}
	if _, snd := a.lastPlay(); snd[0] != "sauti/language" {
		t.Fatalf("first play %v", snd)
	}
	c.Handle(ctx, Event{Type: EventDTMF, ChannelID: "ch1", Digit: "1"})
	// Callback screen: digits accumulate until '#'.
	for _, d := range "0772999888" {
		c.Handle(ctx, Event{Type: EventDTMF, ChannelID: "ch1", Digit: string(d)})
	}
	if len(s.inputs) != 1 {
		t.Fatalf("answered before '#': %v", s.inputs)
	}
	c.Handle(ctx, Event{Type: EventDTMF, ChannelID: "ch1", Digit: "#"})
	if len(s.inputs) != 2 || s.inputs[1] != "0772999888" {
		t.Fatalf("inputs = %v", s.inputs)
	}
	pb, snd := a.lastPlay()
	if !strings.Contains(strings.Join(snd, " "), "sauti/digits/7") || len(a.hangups) != 0 {
		t.Fatalf("closing play %v, hangups %v", snd, a.hangups)
	}
	c.Handle(ctx, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb})
	if len(a.hangups) != 1 {
		t.Fatal("not hung up after the closing message")
	}
	if strings.Contains(logs.String(), "772123456") || strings.Contains(logs.String(), "772999888") {
		t.Fatalf("log carries a number:\n%s", logs.String())
	}
}

// "Press 1 for this number" is a single key when the caller ID is known.
func TestControllerSameNumberIsOneKey(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, _, _ := newController(t, s)
	ctx := context.Background()
	c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	c.Handle(ctx, Event{Type: EventDTMF, ChannelID: "ch1", Digit: "1"})
	c.Handle(ctx, Event{Type: EventDTMF, ChannelID: "ch1", Digit: "1"})
	if len(s.inputs) != 2 || s.inputs[1] != "1" {
		t.Fatalf("inputs = %v", s.inputs)
	}
}

// Silence re-plays the prompt, then the call ends (the abandonment sweeper
// and resume take it from there).
func TestControllerNoInputRepromptsThenHangsUp(t *testing.T) {
	s := &scripted{prompts: prompts()}
	a := &fakeARI{}
	c := New(a, s, slog.New(slog.DiscardHandler), Options{NoInput: 20 * time.Millisecond, MaxReprompts: 2})
	c.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: ""})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		done := len(a.hangups) == 1
		a.mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.hangups) != 1 || len(a.plays) != 3 {
		t.Fatalf("plays %d hangups %d, want 3 plays (prompt + 2 re-prompts) then hang-up", len(a.plays), len(a.hangups))
	}
}

// If intake fails, the caller hears the error message and the call ends; the
// error is logged without the number.
func TestControllerIntakeErrorEndsCallSafely(t *testing.T) {
	s := &scripted{prompts: prompts(), failOn: "1"}
	c, a, logs := newController(t, s)
	ctx := context.Background()
	c.Handle(ctx, Event{Type: EventStasisStart, ChannelID: "ch1", CallerNumber: "+256772123456"})
	c.Handle(ctx, Event{Type: EventDTMF, ChannelID: "ch1", Digit: "1"})
	pb, snd := a.lastPlay()
	if snd[0] != "sauti/en/internal_error" {
		t.Fatalf("error play %v", snd)
	}
	c.Handle(ctx, Event{Type: EventPlaybackFinished, ChannelID: "ch1", PlaybackID: pb})
	if len(a.hangups) != 1 || !strings.Contains(logs.String(), "store down") || strings.Contains(logs.String(), "772123456") {
		t.Fatalf("hangups %v, logs:\n%s", a.hangups, logs.String())
	}
}

func TestControllerIgnoresUnknownChannels(t *testing.T) {
	s := &scripted{prompts: prompts()}
	c, a, _ := newController(t, s)
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "nobody", Digit: "1"})
	if len(a.plays) != 0 || len(s.inputs) != 0 {
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
