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
	AskReasons bool // decision 6 approved
	Language   string
	Log        *slog.Logger
	OpTimeout  time.Duration // each store or ARI operation (default 3 s)
}

type alertCall struct {
	attempt   uuid.UUID
	alert     dispatch.ResponderAlert
	responder uuid.UUID
	reasons   bool // the reason list is being heard
	plays     int
	done      bool
}

// AlertCalls places and runs outbound alert calls.
type AlertCalls struct {
	c     AlertCallsConfig
	mu    sync.Mutex
	calls map[string]*alertCall
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
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &AlertCalls{c: c, calls: map[string]*alertCall{}}, nil
}

// RunOnce calls every alert due a voice call; each attempt is claimed before
// the call (never twice) and settled with the call's placement.
func (a *AlertCalls) RunOnce(ctx context.Context) (int, error) {
	due, err := a.c.Store.DueVoiceAlerts(ctx, a.c.After, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		attempt, fresh, err := a.c.Store.BeginAttempt(ctx, d.AlertID, dispatch.PurposeAlert, dispatch.ChannelVoice)
		if err != nil {
			return n, err
		}
		if !fresh {
			continue
		}
		n++
		ref, cerr := a.dial(ctx, d.ResponderID, attempt)
		status := "sent"
		if cerr != nil {
			status = "unknown"
			if errors.Is(cerr, dispatch.ErrRejected) {
				status = "failed"
			}
			a.c.Log.Error("voice alert not placed", "alert", d.AlertID, "err", cerr)
		}
		if _, err := a.c.Store.FinishAttempt(ctx, attempt, status, ref); err != nil {
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
		if st == nil || st.done {
			return
		}
		a.key(op, e.ChannelID, st, e.Digit)
	case EventStasisEnd, EventHangupRequest:
		a.mu.Lock()
		delete(a.calls, e.ChannelID)
		a.mu.Unlock()
	}
}

func (a *AlertCalls) key(ctx context.Context, ch string, st *alertCall, digit string) {
	if st.reasons {
		reasons := a.c.Copy.DeclineReasons[string(st.alert.Role)]
		if k, err := strconv.Atoi(digit); err == nil && k >= 1 && k <= len(reasons) {
			a.decline(ctx, ch, st, reasons[k-1].Code)
		}
		return
	}
	switch digit {
	case "1":
		out, err := a.c.Store.AcceptAlert(ctx, st.alert.AlertID, st.responder)
		if err != nil {
			a.c.Log.Error("voice accept failed", "alert", st.alert.AlertID, "err", err)
			return
		}
		name := map[dispatch.AcceptResult]string{dispatch.Accepted: "alert-accepted", dispatch.AlreadyTaken: "alert-already-taken",
			dispatch.AlreadyAssigned: "alert-already-assigned"}[out.Result]
		if name == "" {
			name = "alert-no-longer-needed"
		}
		a.finish(ctx, ch, st, name)
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
		return
	}
	name := "alert-declined"
	if out != dispatch.OutcomeDeclined {
		name = "alert-no-longer-needed"
	}
	a.finish(ctx, ch, st, name)
}

func (a *AlertCalls) finish(ctx context.Context, ch string, st *alertCall, sound string) {
	st.done = true
	a.play(ctx, ch, st, []string{a.sound(sound)})
	_ = a.c.ARI.Hangup(ctx, ch)
}

func (a *AlertCalls) play(ctx context.Context, ch string, st *alertCall, sounds []string) {
	st.plays++
	_ = a.c.ARI.Play(ctx, ch, fmt.Sprintf("%s-a%d", ch, st.plays), sounds)
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
		out = append(out, b+"alert-intro", b+"alert-options", b+"alert-accepted", b+"alert-already-taken",
			b+"alert-already-assigned", b+"alert-no-longer-needed", b+"alert-declined")
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
