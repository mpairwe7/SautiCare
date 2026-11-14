package ivr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

// Outbound voice alerts (M2 Part B Task 10; Build Prompt §10.4; scenario 9).
// The alert ladder: SMS at once (dispatch-worker); a voice call when the
// alert is still pending after voice_fallback_after (20 s, decision 8); the
// USSD menu at any time. ivr-controller owns Asterisk, so it places the
// call: it claims the voice attempt (once per alert, never while dispatch is
// suspended), dials the responder's number into its own Stasis application,
// and on answer speaks the alert — press 1 to accept, 2 to decline (then
// the reason once decision 6 allows it), 3 to repeat. Answers go through the
// same dispatch functions as the USSD menu, so the first acceptance per role
// wins across channels. The call records nothing.

// AlertARI is what alert calls need from Asterisk.
type AlertARI interface {
	Originate(ctx context.Context, endpoint, callerID string, args []string, timeout time.Duration) (string, error)
	Answer(ctx context.Context, channelID string) error
	Play(ctx context.Context, channelID, playbackID string, sounds []string) error
	StopPlayback(ctx context.Context, playbackID string) error
	Hangup(ctx context.Context, channelID string) error
}

// AlertStore is the operational store as ivr-controller sees alerts (op_ivr).
type AlertStore interface {
	DueVoiceAlerts(ctx context.Context, after time.Duration, limit int) ([]dispatch.VoiceAlert, error)
	BeginAttempt(ctx context.Context, alert uuid.UUID, purpose, channel string) (uuid.UUID, bool, error)
	FinishAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error)
	ResponderPhone(ctx context.Context, responderID uuid.UUID) (incident.SealedContact, error)
	AlertForAttempt(ctx context.Context, attempt uuid.UUID) (dispatch.ResponderAlert, uuid.UUID, error)
	AcceptAlert(ctx context.Context, alert, responderID uuid.UUID) (dispatch.AcceptOutcome, error)
	DeclineAlert(ctx context.Context, alert, responderID uuid.UUID, reason string) (string, error)
}

// Opener opens a sealed identifier (crypto.Envelope).
type Opener interface {
	Open(ctx context.Context, s crypto.Sealed, aad crypto.AAD) ([]byte, error)
}

// AlertCallsConfig wires alert calls.
type AlertCallsConfig struct {
	ARI        AlertARI
	Store      AlertStore
	Opener     Opener
	Dial       string        // endpoint template with {number}, e.g. PJSIP/{number}@sauti-trunk (P0 provisions the trunk)
	CallerID   string        // the number responders see (Sauti's public number; placeholder until P0)
	After      time.Duration // voice_fallback_after
	Ring       time.Duration // how long a call rings (default 30 s)
	Copy       config.Responder
	AskReasons bool          // decision 6 approved
	Gazetteer  location.File // wards and landmarks with recordings: where to go, spoken after an accept
	Language   string
	Log        *slog.Logger
	OpTimeout  time.Duration // each store or ARI operation (default 3 s)
	// NoInput is how long an answered call waits for a key after the menu
	// ends (default 10 s): the menu is heard once more, then a closing
	// prompt, then the line is freed (ADR-0029 rule 2).
	NoInput time.Duration
}

type alertCall struct {
	attempt   uuid.UUID
	alert     dispatch.ResponderAlert
	responder uuid.UUID
	reasons   bool // the reason list is being heard
	plays     int
	done      bool
	current   string // the prompt being heard: a key cuts it
	hangupOn  string // the closing prompt: hang up once it has been heard
	replays   int    // menus heard again after no input
	timer     *time.Timer
	timerGen  int // bumped whenever the timer is armed or stopped: a stale one does nothing
}

// AlertCalls places and runs outbound alert calls.
type AlertCalls struct {
	c     AlertCallsConfig
	mu    sync.Mutex
	calls map[string]*alertCall
	run   sync.Mutex // serialises the calls' events and their no-input timers
}

// NewAlertCalls validates the wiring.
func NewAlertCalls(c AlertCallsConfig) (*AlertCalls, error) {
	if c.ARI == nil || c.Store == nil || c.Opener == nil || !strings.Contains(c.Dial, "{number}") || c.After <= 0 {
		return nil, errors.New("ivr: alert calls need ARI, a store, an opener, a dial template with {number} and the fallback time")
	}
	if c.Ring <= 0 {
		c.Ring = 30 * time.Second
	}
	if c.Language == "" {
		c.Language = "en"
	}
	if c.OpTimeout <= 0 {
		c.OpTimeout = 3 * time.Second
	}
	if c.NoInput <= 0 {
		c.NoInput = 10 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &AlertCalls{c: c, calls: map[string]*alertCall{}}, nil
}

// RunOnce calls every alert due a voice call; each attempt is claimed before
// the call (never twice) and settled with the call's placement.
func (a *AlertCalls) RunOnce(ctx context.Context) (int, error) {
	due, err := bounded(ctx, a.c.OpTimeout, func(c context.Context) ([]dispatch.VoiceAlert, error) {
		return a.c.Store.DueVoiceAlerts(c, a.c.After, 50)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		claim, err := bounded(ctx, a.c.OpTimeout, func(c context.Context) (claimed, error) {
			id, fresh, err := a.c.Store.BeginAttempt(c, d.AlertID, dispatch.PurposeAlert, dispatch.ChannelVoice)
			return claimed{id, fresh}, err
		})
		if err != nil {
			return n, err
		}
		if !claim.fresh {
			continue
		}
		attempt := claim.id
		n++
		ref, cerr := bounded(ctx, originateTimeout, func(c context.Context) (string, error) { return a.dial(c, d.ResponderID, attempt) })
		status := "sent"
		if cerr != nil {
			status = "unknown"
			if errors.Is(cerr, dispatch.ErrRejected) {
				status = "failed"
			}
			a.c.Log.Error("voice alert not placed", "alert", d.AlertID, "err", cerr)
		}
		if _, err := bounded(context.WithoutCancel(ctx), a.c.OpTimeout, func(c context.Context) (bool, error) {
			return a.c.Store.FinishAttempt(c, attempt, status, ref)
		}); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (a *AlertCalls) dial(ctx context.Context, responderID, attempt uuid.UUID) (string, error) {
	c, err := a.c.Store.ResponderPhone(ctx, responderID)
	if err != nil {
		return "", err
	}
	raw, err := a.c.Opener.Open(ctx, c.Sealed, c.AAD)
	if err != nil {
		return "", err
	}
	p, err := pii.ParsePhone(string(raw))
	if err != nil {
		return "", fmt.Errorf("%w: the stored number is not a phone number", dispatch.ErrRejected)
	}
	endpoint := strings.ReplaceAll(a.c.Dial, "{number}", p.E164())
	return a.c.ARI.Originate(ctx, endpoint, a.c.CallerID, []string{"alert", attempt.String()}, a.c.Ring)
}

// Run calls due alerts every interval until ctx ends (under leadership).
func (a *AlertCalls) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := a.RunOnce(ctx); err != nil && ctx.Err() == nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Handle runs the answered alert calls' events (the alert application's
// event stream). Each operation has its own deadline.
func (a *AlertCalls) Handle(ctx context.Context, e Event) {
	a.run.Lock()
	defer a.run.Unlock()
	op, cancel := context.WithTimeout(ctx, a.c.OpTimeout)
	defer cancel()
	switch e.Type {
	case EventStasisStart:
		if len(e.Args) != 2 || e.Args[0] != "alert" {
			_ = a.c.ARI.Hangup(op, e.ChannelID)
			return
		}
		attempt, err := uuid.Parse(e.Args[1])
		if err != nil {
			_ = a.c.ARI.Hangup(op, e.ChannelID)
			return
		}
		alert, who, err := a.c.Store.AlertForAttempt(op, attempt)
		if err != nil {
			a.c.Log.Error("answered alert call without its alert", "err", err)
			_ = a.c.ARI.Hangup(op, e.ChannelID)
			return
		}
		st := &alertCall{attempt: attempt, alert: alert, responder: who}
		a.mu.Lock()
		a.calls[e.ChannelID] = st
		a.mu.Unlock()
		if err := a.c.ARI.Answer(op, e.ChannelID); err != nil {
			return
		}
		a.play(op, e.ChannelID, st, a.alertSounds(alert))
	case EventDTMF:
		a.mu.Lock()
		st := a.calls[e.ChannelID]
		a.mu.Unlock()
		// A key the menu being heard does not offer is ignored: cutting its
		// prompt would leave the responder in silence.
		if st == nil || st.done || !a.offers(st, e.Digit) {
			return
		}
		a.disarm(st)
		cut(op, a.c.ARI, &st.current)
		a.key(op, e.ChannelID, st, e.Digit)
	case EventPlaybackFinished:
		a.mu.Lock()
		st := a.calls[e.ChannelID]
		a.mu.Unlock()
		if st == nil {
			return
		}
		current := st.current == e.PlaybackID
		if current {
			st.current = ""
		}
		switch {
		case (st.hangupOn != "" && st.hangupOn == e.PlaybackID) || (current && failed(e)):
			_ = a.c.ARI.Hangup(op, e.ChannelID)
		case current && !st.done:
			a.arm(ctx, e.ChannelID, st) // the menu was heard: wait for a key
		}
	case EventStasisEnd, EventHangupRequest:
		a.mu.Lock()
		if st := a.calls[e.ChannelID]; st != nil {
			a.disarm(st)
		}
		delete(a.calls, e.ChannelID)
		a.mu.Unlock()
	}
}

// DisarmAll stops every call's no-input time; outboundTerm calls it when
// the leader's term ends, so an old term's timer never plays or closes a
// call the next leader may serve. The call itself stays up until it ends.
func (a *AlertCalls) DisarmAll() {
	a.run.Lock()
	defer a.run.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, st := range a.calls {
		a.disarm(st)
	}
}

// arm starts the no-input time of a menu just heard.
func (a *AlertCalls) arm(ctx context.Context, ch string, st *alertCall) {
	a.disarm(st)
	gen, bg := st.timerGen, context.WithoutCancel(ctx) // the wait outlives the event
	st.timer = time.AfterFunc(a.c.NoInput, func() { a.noInput(bg, ch, st, gen) })
}

func (a *AlertCalls) disarm(st *alertCall) {
	st.timerGen++
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}

// noInput: no key since the menu ended. The menu is heard once more; after
// that the call closes, freeing the responder's phone and the trunk, with
// nothing recorded as an answer (ADR-0029 rule 2).
func (a *AlertCalls) noInput(ctx context.Context, ch string, st *alertCall, gen int) {
	a.run.Lock()
	defer a.run.Unlock()
	a.mu.Lock()
	live := a.calls[ch] == st
	a.mu.Unlock()
	if !live || st.done || st.timerGen != gen {
		return
	}
	op, cancel := context.WithTimeout(ctx, a.c.OpTimeout)
	defer cancel()
	if st.replays == 0 {
		st.replays++
		if st.reasons {
			a.play(op, ch, st, []string{a.sound("decline-reasons-" + string(st.alert.Role))})
		} else {
			a.play(op, ch, st, a.alertSounds(st.alert))
		}
		return
	}
	a.c.Log.Warn("alert call closed after no input", "alert", st.alert.AlertID)
	a.finish(op, ch, st, a.sound("alert-no-input-closing"))
}

// offers reports whether the menu being heard takes digit: a reason's number
// in the reasons list, else 1, 2 or 3.
func (a *AlertCalls) offers(st *alertCall, digit string) bool {
	if st.reasons {
		k, err := strconv.Atoi(digit)
		return err == nil && k >= 1 && k <= len(a.c.Copy.DeclineReasons[string(st.alert.Role)])
	}
	return digit == "1" || digit == "2" || digit == "3"
}

// key acts on a digit the menu offers (offers).
func (a *AlertCalls) key(ctx context.Context, ch string, st *alertCall, digit string) {
	if st.reasons {
		k, _ := strconv.Atoi(digit)
		a.decline(ctx, ch, st, a.c.Copy.DeclineReasons[string(st.alert.Role)][k-1].Code)
		return
	}
	switch digit {
	case "1":
		out, err := a.c.Store.AcceptAlert(ctx, st.alert.AlertID, st.responder)
		if err != nil {
			a.c.Log.Error("voice accept failed", "alert", st.alert.AlertID, "err", err)
			a.menuAgain(ctx, ch, st)
			return
		}
		name := map[dispatch.AcceptResult]string{dispatch.Accepted: "alert-accepted", dispatch.AlreadyTaken: "alert-already-taken",
			dispatch.AlreadyAssigned: "alert-already-assigned"}[out.Result]
		if name == "" {
			name = "alert-no-longer-needed"
		}
		sounds := []string{a.sound(name)}
		if out.Result == dispatch.Accepted {
			// Where to go, then how to reach the caller: the bridge
			// (Build Prompt §10.4, §10.5).
			sounds = append(append(sounds, a.whereToGo(st.alert.Facts)...), a.sound("alert-bridge-instructions"))
		}
		a.finish(ctx, ch, st, sounds...)
	case "2":
		if !a.c.AskReasons {
			a.decline(ctx, ch, st, "")
			return
		}
		st.reasons = true
		a.play(ctx, ch, st, []string{a.sound("decline-reasons-" + string(st.alert.Role))})
	case "3":
		a.play(ctx, ch, st, a.alertSounds(st.alert))
	}
}

func (a *AlertCalls) decline(ctx context.Context, ch string, st *alertCall, reason string) {
	out, err := a.c.Store.DeclineAlert(ctx, st.alert.AlertID, st.responder, reason)
	if err != nil {
		a.c.Log.Error("voice decline failed", "alert", st.alert.AlertID, "err", err)
		a.menuAgain(ctx, ch, st)
		return
	}
	name := "alert-declined"
	if out != dispatch.OutcomeDeclined {
		name = "alert-no-longer-needed"
	}
	a.finish(ctx, ch, st, a.sound(name))
}

// menuAgain replays the alert after an answer the store refused: the key
// cut the menu and stopped its wait, so without it the answered line would
// stay open. Its end re-arms the no-input time, and the call closes as an
// unanswered one does.
//
// The refused operation may have used up its deadline, so the replay runs
// on a fresh one.
func (a *AlertCalls) menuAgain(ctx context.Context, ch string, st *alertCall) {
	st.reasons = false
	op, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.c.OpTimeout)
	defer cancel()
	a.play(op, ch, st, a.alertSounds(st.alert))
}

// finish plays the result and hangs up once it has been heard
// (PlaybackFinished); a result that cannot be played ends the call at once.
func (a *AlertCalls) finish(ctx context.Context, ch string, st *alertCall, sounds ...string) {
	st.done = true
	st.plays++
	st.hangupOn = endAfter(ctx, a.c.ARI, ch, fmt.Sprintf("%s-a%d", ch, st.plays), sounds)
	st.current = st.hangupOn
}

// whereToGo speaks the incident number, then the ward and the landmark when
// the gazetteer in force has them (a free-text landmark is never spoken): the
// alert channel repeats the location after an accept (Build Prompt §10.4).
func (a *AlertCalls) whereToGo(f dispatch.MessageFacts) []string {
	out := digits(f.Reference)
	if _, ok := a.c.Gazetteer.Ward(f.Ward); ok {
		out = append(out, a.sound("ward-"+f.Ward))
	}
	for _, l := range a.c.Gazetteer.Landmarks {
		if l.ID == f.Landmark {
			out = append(out, a.sound("landmark-"+l.ID))
		}
	}
	return out
}

// play plays a menu. One that cannot start sends no end event to arm the
// no-input time, so it is armed here: the menu is tried again, then the call
// closes, and an answered line is never left open.
func (a *AlertCalls) play(ctx context.Context, ch string, st *alertCall, sounds []string) {
	st.plays++
	pb := fmt.Sprintf("%s-a%d", ch, st.plays)
	if err := a.c.ARI.Play(ctx, ch, pb, sounds); err != nil {
		a.c.Log.Error("alert call prompt not played", "alert", st.alert.AlertID, "err", err)
		a.arm(ctx, ch, st)
		return
	}
	st.current = pb
}

func (a *AlertCalls) sound(name string) string { return "sauti/" + a.c.Language + "/" + name }

// alertSounds speaks the alert: the incident number digit by digit, its
// colour and category, then the keys.
func (a *AlertCalls) alertSounds(al dispatch.ResponderAlert) []string {
	out := []string{a.sound("alert-intro")}
	for _, r := range al.Facts.Reference.Spoken() {
		if r >= '0' && r <= '9' {
			out = append(out, "sauti/digits/"+string(r))
		}
	}
	out = append(out, a.sound("alert-colour-"+string(al.Facts.Severity)))
	if al.Facts.Category != "" {
		out = append(out, a.sound("alert-category-"+al.Facts.Category))
	}
	return append(out, a.sound("alert-options"))
}

// AlertSounds lists every recording an alert call can play, for the prompt
// catalogue.
func AlertSounds(tool1 config.Tool1, copy config.Responder) []string {
	var out []string
	for _, lang := range copy.Languages {
		b := "sauti/" + lang + "/"
		out = append(out, b+"alert-intro", b+"alert-options", b+"alert-accepted", b+"alert-bridge-instructions", b+"alert-already-taken",
			b+"alert-already-assigned", b+"alert-no-longer-needed", b+"alert-declined", b+"alert-no-input-closing")
		for _, sev := range []string{"red", "yellow", "green"} {
			out = append(out, b+"alert-colour-"+sev)
		}
		for _, o := range tool1.Category.Options {
			out = append(out, b+"alert-category-"+o.Value)
		}
		for role := range copy.DeclineReasons {
			out = append(out, b+"decline-reasons-"+role)
		}
	}
	return out
}
