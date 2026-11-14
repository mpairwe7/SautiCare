package ivr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/responder"
)

type alertARI struct {
	mu         sync.Mutex
	originated []string // endpoints
	args       [][]string
	answered   []string
	plays      [][]string
	pbs        []string // playback IDs, in order
	hungUp     []string
	stopped    []string
	failPlay   bool
	// onOriginate runs before Originate returns the new channel (an answer
	// event handled before the caller stores it).
	onOriginate func(channelID string)
	gone        map[string]bool // channels that no longer exist (ChannelExists)
	existsErr   error           // ChannelExists fails
}

func (a *alertARI) StopPlayback(_ context.Context, pb string) error {
	a.stopped = append(a.stopped, pb)
	return nil
}

func (a *alertARI) Originate(_ context.Context, endpoint, _ string, args []string, _ time.Duration) (string, error) {
	a.mu.Lock()
	a.originated = append(a.originated, endpoint)
	a.args = append(a.args, args)
	id := fmt.Sprintf("out-%d", len(a.originated))
	hook := a.onOriginate
	a.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return id, nil
}
func (a *alertARI) ChannelExists(_ context.Context, ch string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.existsErr != nil {
		return false, a.existsErr
	}
	return !a.gone[ch], nil
}
func (a *alertARI) Answer(_ context.Context, ch string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answered = append(a.answered, ch)
	return nil
}
func (a *alertARI) Play(_ context.Context, _, pb string, sounds []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failPlay {
		return errors.New("playback refused")
	}
	a.plays = append(a.plays, sounds)
	a.pbs = append(a.pbs, pb)
	return nil
}

// finish reports the last prompt as played to the end.
func (a *alertARI) finish(t *testing.T, h interface{ Handle(context.Context, Event) }, ch string) {
	t.Helper()
	a.mu.Lock()
	pb := a.pbs[len(a.pbs)-1]
	a.mu.Unlock()
	h.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: ch, PlaybackID: pb})
}

// played copies the prompts played so far; timer goroutines append to them.
func (a *alertARI) played() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.plays)
}
func (a *alertARI) Hangup(_ context.Context, ch string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hungUp = append(a.hungUp, ch)
	return nil
}

// hangups counts the hang-ups of ch (timers hang up from their own goroutine).
func (a *alertARI) hangups(ch string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, h := range a.hungUp {
		if h == ch {
			n++
		}
	}
	return n
}

type alertStore struct {
	due      []dispatch.VoiceAlert
	alert    dispatch.ResponderAlert
	who      uuid.UUID
	claimed  map[uuid.UUID]uuid.UUID // alert → attempt
	settled  map[uuid.UUID]string
	accept   dispatch.AcceptResult
	accepted int
	declined map[uuid.UUID]string
}

func (s *alertStore) DueVoiceAlerts(context.Context, time.Duration, int) ([]dispatch.VoiceAlert, error) {
	var out []dispatch.VoiceAlert
	for _, d := range s.due {
		if _, ok := s.claimed[d.AlertID]; !ok {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *alertStore) BeginAttempt(_ context.Context, alert uuid.UUID, _, channel string) (uuid.UUID, bool, error) {
	if channel != dispatch.ChannelVoice {
		panic("not a voice attempt")
	}
	if id, ok := s.claimed[alert]; ok {
		return id, false, nil
	}
	id := uuid.NewV7()
	s.claimed[alert] = id
	return id, true, nil
}
func (s *alertStore) FinishAttempt(_ context.Context, att uuid.UUID, status, _ string) (bool, error) {
	s.settled[att] = status
	return true, nil
}
func (s *alertStore) ResponderPhone(_ context.Context, id uuid.UUID) (incident.SealedContact, error) {
	return incident.SealedContact{Sealed: crypto.Sealed{KeyID: "k", Ciphertext: []byte("+256772000301")},
		AAD: crypto.AAD{Store: "operational", Table: "responders", Column: "phone", RowID: id.String()}}, nil
}
func (s *alertStore) AlertForAttempt(context.Context, uuid.UUID) (dispatch.ResponderAlert, uuid.UUID, error) {
	return s.alert, s.who, nil
}
func (s *alertStore) AcceptAlert(context.Context, uuid.UUID, uuid.UUID) (dispatch.AcceptOutcome, error) {
	s.accepted++
	return dispatch.AcceptOutcome{Result: s.accept}, nil
}
func (s *alertStore) DeclineAlert(_ context.Context, a, _ uuid.UUID, reason string) (string, error) {
	s.declined[a] = reason
	return dispatch.OutcomeDeclined, nil
}

type plainOpener struct{}

func (plainOpener) Open(_ context.Context, s crypto.Sealed, _ crypto.AAD) ([]byte, error) {
	return s.Ciphertext, nil
}

func alertRig(t *testing.T, askReasons bool) (*AlertCalls, *alertARI, *alertStore) {
	t.Helper()
	b, _ := repoForAlerts(t)
	g := gazetteer(t)
	alert := dispatch.ResponderAlert{AlertID: uuid.NewV7(), IncidentID: uuid.NewV7(), Role: responder.RoleTransporter, Outcome: dispatch.OutcomePending,
		Facts: dispatch.MessageFacts{Reference: "1234567894", Severity: incident.SevRed, Category: "trauma", Ward: "w01", Landmark: "w01_market"}}
	st := &alertStore{due: []dispatch.VoiceAlert{{AlertID: alert.AlertID, ResponderID: uuid.NewV7()}}, alert: alert, who: uuid.NewV7(),
		claimed: map[uuid.UUID]uuid.UUID{}, settled: map[uuid.UUID]string{}, declined: map[uuid.UUID]string{}, accept: dispatch.Accepted}
	a := &alertARI{}
	c, err := NewAlertCalls(AlertCallsConfig{ARI: a, Store: st, Opener: plainOpener{}, Dial: "PJSIP/{number}@sauti-trunk",
		After: 20 * time.Second, Copy: b.Responder, AskReasons: askReasons, Gazetteer: g})
	if err != nil {
		t.Fatal(err)
	}
	return c, a, st
}

// Scenario 9's voice rung: an alert still unanswered after 20 s is called
// once, to the responder's own number, through the configured trunk; the
// attempt is claimed before the call and settled with its result.
func TestAlertCallsDialDueAlertsOnce(t *testing.T) {
	c, a, st := alertRig(t, false)
	for range 2 {
		if _, err := c.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.originated) != 1 || a.originated[0] != "PJSIP/+256772000301@sauti-trunk" || a.args[0][0] != "alert" {
		t.Fatalf("originated %v %v", a.originated, a.args)
	}
	att := st.claimed[st.alert.AlertID]
	if a.args[0][1] != att.String() || st.settled[att] != "sent" {
		t.Fatalf("attempt %v settled %q", att, st.settled[att])
	}
}

func answer(t *testing.T, c *AlertCalls, a *alertARI, st *alertStore) string {
	t.Helper()
	if _, err := c.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ch := "out-1"
	c.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: ch, Args: a.args[0]})
	return ch
}

// The answered call hears the alert (incident number digit by digit,
// colour, category) and the keys; 1 accepts, the result is spoken, the call
// ends; 3 repeats.
func TestAlertCallAccept(t *testing.T) {
	c, a, st := alertRig(t, false)
	ch := answer(t, c, a, st)
	if len(a.answered) != 1 || len(a.plays) != 1 || !slices.Contains(a.plays[0], "sauti/digits/7") ||
		!slices.Contains(a.plays[0], "sauti/en/alert-colour-red") || !slices.Contains(a.plays[0], "sauti/en/alert-options") {
		t.Fatalf("answered %v plays %v", a.answered, a.plays)
	}
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "3"})
	if len(a.plays) != 2 || !slices.Equal(a.plays[1], a.plays[0]) {
		t.Fatalf("repeat %v", a.plays)
	}
	// A key cuts the prompt being heard: the answer is spoken at once, not
	// queued behind the rest of the alert.
	if !slices.Equal(a.stopped, a.pbs[:1]) {
		t.Fatalf("stopped %v, want the first prompt %v", a.stopped, a.pbs[:1])
	}
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "1"})
	// C1: the result says where to go — the ward and the landmark — and (I2)
	// the call ends only once it has been heard.
	last := a.plays[len(a.plays)-1]
	if st.accepted != 1 || !slices.Contains(last, "sauti/en/alert-accepted") || !slices.Contains(last, "sauti/en/ward-w01") ||
		!slices.Contains(last, "sauti/en/landmark-w01_market") || !slices.Contains(last, "sauti/digits/4") ||
		last[len(last)-1] != "sauti/en/alert-bridge-instructions" || slices.Contains(a.hungUp, ch) {
		t.Fatalf("accept: %d plays %v hung up %v", st.accepted, a.plays, a.hungUp)
	}
	c.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: ch, PlaybackID: "another"})
	if slices.Contains(a.hungUp, ch) {
		t.Fatal("hung up on another prompt's end")
	}
	a.finish(t, c, ch)
	if !slices.Contains(a.hungUp, ch) {
		t.Fatal("not hung up after the result was heard")
	}
}

// A landmark that is not in the gazetteer in force (free text) is never
// spoken; a result prompt that cannot be played ends the call at once.
func TestAlertCallResultEdges(t *testing.T) {
	c, a, st := alertRig(t, false)
	st.alert.Facts.Landmark = "behind the big tree"
	ch := answer(t, c, a, st)
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "1"})
	for _, s := range a.plays[len(a.plays)-1] {
		if strings.Contains(s, "landmark-") {
			t.Fatalf("free-text landmark spoken: %v", a.plays)
		}
	}
	c, a, st = alertRig(t, false)
	ch = answer(t, c, a, st)
	a.failPlay = true
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "1"})
	if !slices.Contains(a.hungUp, ch) {
		t.Fatal("unplayable result left the call open")
	}
}

// 2 declines: before the amendment with no reason; after it, the role's
// closed list is spoken and the key picks the reason (scenario 10 by voice).
func TestAlertCallDecline(t *testing.T) {
	c, a, st := alertRig(t, false)
	ch := answer(t, c, a, st)
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "2"})
	a.finish(t, c, ch)
	if r, ok := st.declined[st.alert.AlertID]; !ok || r != "" || !slices.Contains(a.hungUp, ch) {
		t.Fatalf("protocol decline %v hung up %v", st.declined, a.hungUp)
	}
	c, a, st = alertRig(t, true)
	ch = answer(t, c, a, st)
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "2"})
	if last := a.plays[len(a.plays)-1]; !strings.HasPrefix(last[0], "sauti/en/decline-reasons-transporter") {
		t.Fatalf("reasons prompt %v", last)
	}
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "3"})
	if st.declined[st.alert.AlertID] != "bike_or_fuel" {
		t.Fatalf("declined %v", st.declined)
	}
}

func repoForAlerts(t *testing.T) (config.Bundle, error) {
	t.Helper()
	b, err := config.Load(os.DirFS("../../../config"))
	if err != nil {
		t.Fatal(err)
	}
	return b, nil
}

// CodeRabbit PR #11 (4560453615): a key the menu being heard does not offer
// leaves its prompt playing (no silence): 9 during the alert, 9 during the
// reasons. Valid keys still cut the prompt.
func TestAnInvalidKeyDoesNotCutTheAlertPrompt(t *testing.T) {
	c, a, st := alertRig(t, true)
	ch := answer(t, c, a, st)
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "9"})
	if len(a.stopped) != 0 || len(a.plays) != 1 || st.accepted != 0 {
		t.Fatalf("invalid key during the alert: stopped %v plays %d", a.stopped, len(a.plays))
	}
	c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: "2"})
	if !slices.Equal(a.stopped, a.pbs[:1]) || len(a.plays) != 2 {
		t.Fatalf("2 did not cut the alert for the reasons: stopped %v plays %v", a.stopped, a.plays)
	}
	for _, k := range []string{"9", "0", "*"} {
		c.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: k})
	}
	if len(a.stopped) != 1 || len(st.declined) != 0 {
		t.Fatalf("invalid keys during the reasons: stopped %v declined %v", a.stopped, st.declined)
	}
}

// An alert whose recording failed to play was never heard: the call ends
// rather than waiting for a key, and nothing is recorded as an answer; the
// alert stays pending for the rest of the ladder (F-03).
func TestAFailedAlertPromptHangsUp(t *testing.T) {
	c, a, st := alertRig(t, false)
	ch := answer(t, c, a, st)
	c.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: ch, PlaybackID: a.pbs[0], PlaybackState: ari.PlaybackFailed})
	if a.hangups(ch) != 1 || st.accepted != 0 || len(st.declined) != 0 {
		t.Fatalf("hangups %d accepted %d declined %v", a.hangups(ch), st.accepted, st.declined)
	}
}

// ADR-0029 rule 2: an answered alert call that hears no key is told the
// alert once more, then a short closing prompt, then the line is freed.
// Nothing is recorded as an answer: the alert stays pending for the ladder.
func TestAnUnansweredAlertCallRepeatsThenHangsUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a, st := alertRig(t, false)
		ch := answer(t, c, a, st)
		first := a.played()[0]
		a.finish(t, c, ch) // heard to the end; no key
		time.Sleep(10*time.Second - time.Millisecond)
		synctest.Wait()
		if len(a.played()) != 1 {
			t.Fatalf("replayed before the no-input time: %v", a.played())
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if len(a.played()) != 2 || !slices.Equal(a.played()[1], first) {
			t.Fatalf("plays %v, want the alert again", a.played())
		}
		a.finish(t, c, ch)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if last := a.played()[len(a.played())-1]; !slices.Equal(last, []string{"sauti/en/alert-no-input-closing"}) || a.hangups(ch) != 0 {
			t.Fatalf("plays %v hangups %d", a.played(), a.hangups(ch))
		}
		a.finish(t, c, ch)
		if a.hangups(ch) != 1 || st.accepted != 0 || len(st.declined) != 0 {
			t.Fatalf("hangups %d accepted %d declined %v", a.hangups(ch), st.accepted, st.declined)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if len(a.played()) != 3 {
			t.Fatalf("played on after closing: %v", a.played())
		}
	})
}
