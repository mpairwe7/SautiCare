package ivr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/responder"
)

// The masked bridge (M2 Part C Task 13; Build Prompt §10.5; ADR-0028).
//
// A responder calls the bridge number. Sauti recognises them by their
// number's keyed hash and their open assignments, asks for the last 4 digits
// of the incident number when they hold several, and calls the incident's
// caller, showing Sauti's number. The caller can call back the same way: they
// are recognised by the number they reported from and reach the assigned
// responder (choosing health worker or transport when both are assigned).
// The two legs meet in a mixing bridge. Nothing is recorded, every bridge
// call is logged with its times and result, and neither party's number ever
// reaches the other party, a log line or a payload (hard constraint 5): a
// number is opened only to dial it.

// BridgeStore is the store as the masked bridge sees it (op_ivr).
type BridgeStore interface {
	ResponderByPhone(ctx context.Context, macs []crypto.MAC) (uuid.UUID, bool, error)
	BridgeTargetsForResponder(ctx context.Context, responderID uuid.UUID) ([]dispatch.BridgeTarget, error)
	BridgeTargetsForCaller(ctx context.Context, macs []crypto.MAC) ([]dispatch.BridgeTarget, error)
	CallerContact(ctx context.Context, incidentID uuid.UUID) (incident.SealedContact, bool, error)
	ResponderPhone(ctx context.Context, responderID uuid.UUID) (incident.SealedContact, error)
	BeginBridgeCall(ctx context.Context, incidentID uuid.UUID, initiatedBy string) (uuid.UUID, error)
	BridgeCallAnswered(ctx context.Context, id uuid.UUID) error
	FinishBridgeCall(ctx context.Context, id uuid.UUID, result string) error
}

// BridgeARI is Asterisk as the masked bridge uses it: outbound calls plus a
// mixing bridge (ari.Client).
type BridgeARI interface {
	AlertARI
	Continue(ctx context.Context, channelID, dialContext, extension string, priority int) error
	SetCallerID(ctx context.Context, channelID, number string) error
	CreateBridge(ctx context.Context, bridgeID string) error
	AddToBridge(ctx context.Context, bridgeID, channelID string) error
	DestroyBridge(ctx context.Context, bridgeID string) error
}

// BridgeKeys computes the lookup hashes of a number (crypto.Identifiers).
type BridgeKeys interface {
	PhoneLookupMACs(ctx context.Context, p pii.Phone) ([]crypto.MAC, error)
}

// BridgeConfig wires the masked bridge.
type BridgeConfig struct {
	ARI      BridgeARI
	Store    BridgeStore
	Keys     BridgeKeys
	Opener   Opener
	Dial     string        // endpoint template with {number}
	CallerID string        // the number the other party sees: Sauti's (placeholder until P0)
	Ring     time.Duration // how long the other party's phone rings (default 30 s)
	// NoAnswerAfter ends a leg nobody answered (default Ring + 5 s).
	NoAnswerAfter time.Duration
	// NoInput is how long a choice waits for a key, counted from the end of
	// its prompt or from the last key (default 15 s, as on the toll-free
	// line): a silent choice is asked again, and after choiceTries a
	// coordinator takes the call.
	NoInput time.Duration
	// PromptMax is the longest a bridge prompt plays (default 30 s). The
	// wait armed when a prompt starts is PromptMax + NoInput, so a prompt
	// whose end event is lost still ends the wait.
	PromptMax time.Duration
	Language  string
	Log       *slog.Logger
	OpTimeout time.Duration
}

// choiceTries is how many times a choice (the last 4 digits, a role, a
// party) may be keyed wrongly or go unanswered before a coordinator takes
// the call.
const choiceTries = 3

// choicePrompt is each choice's prompt, asked again after a silent try.
var choicePrompt = map[string]string{"side": "bridge-choose-side", "role": "bridge-choose-role", "last4": "bridge-last-four"}

// EventChannelDestroyed: a channel ended, including a leg never answered.
const EventChannelDestroyed = ari.EventChannelDestroyed

type bridgeCall struct {
	inbound  string // the channel that called the bridge number
	leg      string // the call Sauti placed to the other party
	id       uuid.UUID
	bridgeID string
	by       string // initiated_by
	targets  []dispatch.BridgeTarget
	asResp   []dispatch.BridgeTarget // a responder's own assignments
	asCaller []dispatch.BridgeTarget // the responders coming to this number's incidents
	choosing string                  // "last4", "role" or "side" while a choice is awaited
	digits   string
	tries    int
	answered bool
	done     bool
	plays    int
	current  string
	hangupOn string
	timer    *time.Timer // the other party's ring time
	wait     *time.Timer // a choice's no-input time, or the closing prompt's hang-up backstop
	waitGen  int         // bumped whenever the wait is started again or stopped: a stale timer does nothing
}

// Bridge runs bridge calls: the bridge number's inbound calls and the legs
// Sauti places for them, on the outbound application.
type Bridge struct {
	c     BridgeConfig
	mu    sync.Mutex // serialises events and the no-answer timers
	calls map[string]*bridgeCall
	byID  map[uuid.UUID]*bridgeCall
}

// NewBridge validates the wiring.
func NewBridge(c BridgeConfig) (*Bridge, error) {
	if c.ARI == nil || c.Store == nil || c.Keys == nil || c.Opener == nil || !strings.Contains(c.Dial, "{number}") || c.CallerID == "" {
		return nil, errors.New("ivr: the bridge needs ARI, a store, keys, an opener, a dial template with {number} and Sauti's number")
	}
	c.Ring, c.Language, c.Log, c.OpTimeout = defaults(c.Ring, c.Language, c.Log, c.OpTimeout)
	if c.NoAnswerAfter <= 0 {
		c.NoAnswerAfter = c.Ring + 5*time.Second
	}
	if c.NoInput <= 0 {
		c.NoInput = 15 * time.Second
	}
	if c.PromptMax <= 0 {
		c.PromptMax = 30 * time.Second
	}
	return &Bridge{c: c, calls: map[string]*bridgeCall{}, byID: map[uuid.UUID]*bridgeCall{}}, nil
}

// Handle runs one event of a bridge call.
func (b *Bridge) Handle(ctx context.Context, e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch e.Type {
	case EventStasisStart:
		switch {
		case len(e.Args) == 1 && e.Args[0] == "bridge":
			b.start(ctx, e)
		case len(e.Args) == 2 && e.Args[0] == "bridge-leg":
			b.legAnswered(ctx, e)
		default:
			b.hangup(ctx, e.ChannelID)
		}
	case EventDTMF:
		// Only a key the choice offers cuts the prompt: any other is not
		// input and leaves the prompt playing.
		if st := b.calls[e.ChannelID]; st != nil && !st.done && e.ChannelID == st.inbound && offered(st.choosing, e.Digit) {
			b.cut(ctx, st)
			b.key(ctx, st, e.Digit)
		}
	case EventPlaybackFinished:
		if st := b.calls[e.ChannelID]; st != nil {
			if st.current == e.PlaybackID {
				st.current = ""
				if !st.done && st.choosing != "" {
					b.arm(ctx, st, b.c.NoInput) // the prompt was heard: the wait runs from its end
				}
			}
			if st.hangupOn != "" && st.hangupOn == e.PlaybackID {
				b.disarm(st)
				b.hangup(ctx, e.ChannelID)
			}
		}
	case EventChannelDestroyed:
		// The other party's leg ended before any answer (a phone off or
		// unreachable): no answer, at once rather than at the ring timeout.
		if st := b.calls[e.ChannelID]; st != nil && e.ChannelID == st.leg && !st.answered && !st.done {
			delete(b.calls, e.ChannelID)
			st.done = true
			if st.timer != nil {
				st.timer.Stop()
			}
			b.teardown(ctx, st, dispatch.BridgeNoAnswer)
			b.closing(ctx, st, "bridge-no-answer")
			return
		}
		if st := b.calls[e.ChannelID]; st != nil {
			b.ended(ctx, st, e.ChannelID)
		}
	case EventStasisEnd, EventHangupRequest:
		if st := b.calls[e.ChannelID]; st != nil {
			b.ended(ctx, st, e.ChannelID)
		}
	}
}

func (b *Bridge) op(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, b.c.OpTimeout)
}

// start recognises who is calling and what they may be connected to. A
// failure to look them up hands the call to a coordinator (rule 14); only a
// definite "nothing open" is answered as such.
func (b *Bridge) start(ctx context.Context, e Event) {
	st := &bridgeCall{inbound: e.ChannelID}
	b.calls[e.ChannelID] = st
	octx, cancel := b.op(ctx)
	defer cancel()
	if err := b.c.ARI.Answer(octx, e.ChannelID); err != nil {
		b.c.Log.Error("bridge call not answered", "err", err)
		b.toCoordinator(ctx, st)
		return
	}
	asResp, asCaller, err := b.recognise(octx, e.CallerNumber)
	if err != nil {
		b.c.Log.Error("bridge caller not recognised", "err", err)
		b.toCoordinator(ctx, st)
		return
	}
	st.asResp, st.asCaller = asResp, asCaller
	switch {
	case len(asResp) == 0 && len(asCaller) == 0:
		// The same answer for an unknown number and one with nothing open:
		// the bridge never tells who is registered.
		b.finishWith(ctx, st, "bridge-no-assignment")
	case len(asResp) > 0 && len(asCaller) > 0:
		// A responder who also reported an emergency: which party?
		st.choosing = "side"
		b.ask(ctx, st, "bridge-choose-side")
	case len(asResp) > 0:
		b.proceed(ctx, st, asResp, roleBy(asResp[0].Role))
	default:
		b.proceed(ctx, st, asCaller, dispatch.BridgeByCaller)
	}
}

// proceed connects at once, or asks which helper (a caller with both roles
// assigned) or which incident (the last 4 digits: a responder with several
// assignments, or a caller whose helpers of one role are on several
// incidents).
func (b *Bridge) proceed(ctx context.Context, st *bridgeCall, targets []dispatch.BridgeTarget, by string) {
	st.targets, st.by, st.tries = targets, by, 0
	switch {
	case len(targets) == 1:
		st.choosing = ""
		b.connect(ctx, st, targets[0])
	case by == dispatch.BridgeByCaller && slices.ContainsFunc(targets, func(t dispatch.BridgeTarget) bool { return t.Role != targets[0].Role }):
		st.choosing = "role"
		b.ask(ctx, st, "bridge-choose-role")
	default:
		st.choosing = "last4"
		b.ask(ctx, st, "bridge-last-four")
	}
}

// recognise finds what this number may reach: as a responder, the callers of
// the incidents they hold an open assignment on; as a caller, the responders
// assigned to the open incidents it reported. Nobody reaches themselves: an
// incident the responder reported is not one of their callers, and they are
// not one of their own helpers.
func (b *Bridge) recognise(ctx context.Context, from string) (asResp, asCaller []dispatch.BridgeTarget, err error) {
	p, perr := pii.ParsePhone(from)
	if perr != nil {
		// A withheld or malformed number is simply not recognised: the
		// caller hears the same "nothing to connect" as an unknown number.
		return nil, nil, nil //nolint:nilerr // not recognising a caller is not a failure
	}
	macs, err := b.c.Keys.PhoneLookupMACs(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	self, isResponder, err := b.c.Store.ResponderByPhone(ctx, macs)
	if err != nil {
		return nil, nil, err
	}
	reportedAll, err := b.c.Store.BridgeTargetsForCaller(ctx, macs)
	if err != nil {
		return nil, nil, err
	}
	reported := map[uuid.UUID]bool{}
	for _, t := range reportedAll {
		reported[t.IncidentID] = true
		if !isResponder || t.ResponderID != self {
			asCaller = append(asCaller, t)
		}
	}
	if isResponder {
		mine, err := b.c.Store.BridgeTargetsForResponder(ctx, self)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range mine {
			if !reported[t.IncidentID] {
				asResp = append(asResp, t)
			}
		}
	}
	return asResp, asCaller, nil
}

func roleBy(r responder.Role) string {
	if r == responder.RoleTransporter {
		return dispatch.BridgeByTransporter
	}
	return dispatch.BridgeByVHT
}

// offered reports whether the choice under way takes digit: 1 or 2 for a
// party or a role, any digit for the last 4 digits.
func offered(choosing, digit string) bool {
	switch choosing {
	case "side", "role":
		return digit == "1" || digit == "2"
	case "last4":
		return len(digit) == 1 && digit[0] >= '0' && digit[0] <= '9'
	}
	return false
}

// key takes a key the choice offers: the party (a responder who also
// reported an emergency), the role (a caller with both roles assigned) or one
// of the last 4 digits (a responder with several assignments). It is input:
// the wait starts again from it.
func (b *Bridge) key(ctx context.Context, st *bridgeCall, digit string) {
	b.arm(ctx, st, b.c.NoInput)
	switch st.choosing {
	case "side":
		if digit == "1" {
			b.proceed(ctx, st, st.asResp, roleBy(st.asResp[0].Role))
		} else {
			b.proceed(ctx, st, st.asCaller, dispatch.BridgeByCaller)
		}
		return
	case "role":
		want := map[string]responder.Role{"1": responder.RoleVHT, "2": responder.RoleTransporter}[digit]
		var match []dispatch.BridgeTarget
		for _, t := range st.targets {
			if t.Role == want {
				match = append(match, t)
			}
		}
		if len(match) == 0 {
			b.again(ctx, st, "bridge-choose-role")
			return
		}
		// Helpers of that role on several incidents: which incident?
		b.proceed(ctx, st, match, st.by)
		return
	}
	st.digits += digit
	if len(st.digits) < 4 {
		return
	}
	var match []dispatch.BridgeTarget
	for _, t := range st.targets {
		if strings.HasSuffix(string(t.Reference), st.digits) {
			match = append(match, t)
		}
	}
	st.digits = ""
	if len(match) == 1 {
		st.choosing = ""
		if st.by != dispatch.BridgeByCaller {
			st.by = roleBy(match[0].Role) // a responder's role on that incident
		}
		b.connect(ctx, st, match[0])
		return
	}
	b.again(ctx, st, "bridge-last-four-again")
}

// again counts a wrong or silent try: the choice is asked again, and after
// choiceTries a coordinator takes the call (rule 14).
func (b *Bridge) again(ctx context.Context, st *bridgeCall, sound string) {
	st.digits = ""
	if st.tries++; st.tries >= choiceTries {
		b.toCoordinator(ctx, st)
		return
	}
	b.ask(ctx, st, sound)
}

// ask plays a choice's prompt and starts its wait, long enough for the
// prompt and the no-input time after it. A prompt that cannot be played
// leaves the caller without instructions, so a coordinator takes the call at
// once (rule 14), whatever tries remain.
func (b *Bridge) ask(ctx context.Context, st *bridgeCall, sound string) {
	if err := b.play(ctx, st, b.sound(sound)); err != nil {
		b.c.Log.Error("bridge prompt not played; to a coordinator", "prompt", sound, "err", err)
		b.toCoordinator(ctx, st)
		return
	}
	b.arm(ctx, st, b.c.PromptMax+b.c.NoInput)
}

// arm starts the call's wait again, ending d from now. It runs under b.mu,
// and so does the timer's work (noInput).
func (b *Bridge) arm(ctx context.Context, st *bridgeCall, d time.Duration) {
	b.disarm(st)
	gen, bg := st.waitGen, context.WithoutCancel(ctx) // the wait outlives the event
	st.wait = time.AfterFunc(d, func() { b.noInput(bg, st, gen) })
}

// disarm stops the call's wait; a timer already firing finds its generation
// gone and does nothing.
func (b *Bridge) disarm(st *bridgeCall) {
	st.waitGen++
	if st.wait != nil {
		st.wait.Stop()
		st.wait = nil
	}
}

// noInput ends a wait that ran out: a choice heard no key it offers (a
// silent try: asked again, or a coordinator after choiceTries), or a closing
// prompt's end never arrived (the call is hung up all the same).
func (b *Bridge) noInput(ctx context.Context, st *bridgeCall, gen int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st.waitGen != gen {
		return
	}
	st.wait = nil
	switch {
	case st.hangupOn != "":
		b.hangup(ctx, st.inbound)
	case !st.done && st.choosing != "":
		octx, cancel := b.op(ctx)
		defer cancel()
		b.cut(octx, st)
		b.again(ctx, st, choicePrompt[st.choosing])
	}
}

// connect logs the bridge call, puts the caller in a mixing bridge with a
// hold prompt and calls the other party, showing Sauti's number.
func (b *Bridge) connect(ctx context.Context, st *bridgeCall, t dispatch.BridgeTarget) {
	b.disarm(st) // the choice is made
	octx, cancel := b.op(ctx)
	defer cancel()
	id, err := b.c.Store.BeginBridgeCall(octx, t.IncidentID, st.by)
	if err != nil {
		b.c.Log.Error("bridge call not begun", "incident", t.IncidentID, "err", err)
		b.toCoordinator(ctx, st)
		return
	}
	st.id, st.bridgeID = id, "bridge-"+id.String()
	b.byID[id] = st
	endpoint, err := b.endpoint(octx, st.by, t)
	if err == nil {
		err = b.c.ARI.CreateBridge(octx, st.bridgeID)
	}
	if err == nil {
		// Rule 5: the calling party presents Sauti's number from here on, so
		// joining the bridge never sends the other party theirs as a
		// connected-line update.
		err = b.c.ARI.SetCallerID(octx, st.inbound, b.c.CallerID)
	}
	if err == nil {
		err = b.c.ARI.AddToBridge(octx, st.bridgeID, st.inbound)
	}
	if err != nil {
		b.fail(ctx, st, err)
		return
	}
	// The connecting prompt, then ringback until the other party answers
	// (never dead air, which a basic-phone user takes for a dropped call).
	if err := b.play(ctx, st, b.sound("bridge-connecting"), ari.RingbackTone); err != nil {
		// The other party is called all the same; the caller hears silence
		// until the answer.
		b.c.Log.Warn("bridge connecting prompt not played", "bridge_call", id, "err", err)
	}
	octx2, cancel2 := context.WithTimeout(ctx, originateTimeout)
	defer cancel2()
	leg, err := b.c.ARI.Originate(octx2, endpoint, b.c.CallerID, []string{"bridge-leg", id.String()}, b.c.Ring)
	if err != nil {
		b.fail(ctx, st, err)
		return
	}
	st.leg = leg
	b.calls[leg] = st
	st.timer = time.AfterFunc(b.c.NoAnswerAfter, func() { b.noAnswer(context.WithoutCancel(ctx), st) })
	b.c.Log.Info("bridge call placed", "bridge_call", id, "incident", t.IncidentID, "by", st.by)
}

// endpoint opens the other party's number only to dial it.
func (b *Bridge) endpoint(ctx context.Context, by string, t dispatch.BridgeTarget) (string, error) {
	var c incident.SealedContact
	var err error
	if by == dispatch.BridgeByCaller {
		c, err = b.c.Store.ResponderPhone(ctx, t.ResponderID)
	} else {
		var ok bool
		c, ok, err = b.c.Store.CallerContact(ctx, t.IncidentID)
		if err == nil && !ok {
			err = errors.New("the incident has no number to call back")
		}
	}
	if err != nil {
		return "", err
	}
	raw, err := b.c.Opener.Open(ctx, c.Sealed, c.AAD)
	if err != nil {
		return "", err
	}
	p, err := pii.ParsePhone(string(raw))
	if err != nil {
		return "", fmt.Errorf("%w: the stored number is not a phone number", dispatch.ErrRejected)
	}
	return strings.ReplaceAll(b.c.Dial, "{number}", p.E164()), nil
}

// legAnswered joins the answered party to the bridge.
func (b *Bridge) legAnswered(ctx context.Context, e Event) {
	id, err := uuid.Parse(e.Args[1])
	st := b.byID[id]
	if err != nil || st == nil || st.done {
		b.hangup(ctx, e.ChannelID) // not a call this process placed, or already over
		return
	}
	st.leg, st.answered = e.ChannelID, true
	b.calls[e.ChannelID] = st
	if st.timer != nil {
		st.timer.Stop()
	}
	b.cut(ctx, st)
	octx, cancel := b.op(ctx)
	defer cancel()
	if err := b.c.ARI.AddToBridge(octx, st.bridgeID, e.ChannelID); err != nil {
		b.fail(ctx, st, err)
		return
	}
	if err := b.c.Store.BridgeCallAnswered(octx, st.id); err != nil {
		b.c.Log.Error("bridge answer not logged", "bridge_call", st.id, "err", err)
	}
}

// ended: either party hung up — the other is hung up and the call logged.
func (b *Bridge) ended(ctx context.Context, st *bridgeCall, ch string) {
	delete(b.calls, ch)
	b.disarm(st)
	if st.done {
		return
	}
	st.done = true
	if st.timer != nil {
		st.timer.Stop()
	}
	result := dispatch.BridgeNoAnswer
	if st.answered {
		result = dispatch.BridgeConnected
	}
	b.teardown(ctx, st, result)
	if other := map[bool]string{true: st.leg, false: st.inbound}[ch == st.inbound]; other != "" {
		b.hangup(ctx, other)
	}
}

// noAnswer: the other party's phone rang out.
func (b *Bridge) noAnswer(ctx context.Context, st *bridgeCall) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st.done || st.answered {
		return
	}
	st.done = true
	b.teardown(ctx, st, dispatch.BridgeNoAnswer)
	if st.leg != "" {
		b.hangup(ctx, st.leg)
	}
	b.closing(ctx, st, "bridge-no-answer")
}

// fail ends a bridge call that could not be placed: a coordinator takes the
// caller (rule 14), and an answered other party is not left in silence.
func (b *Bridge) fail(ctx context.Context, st *bridgeCall, err error) {
	b.c.Log.Error("bridge call failed", "bridge_call", st.id, "err", err)
	if st.timer != nil {
		st.timer.Stop()
	}
	b.teardown(ctx, st, dispatch.BridgeFailed)
	if st.leg != "" {
		b.hangup(ctx, st.leg)
	}
	b.toCoordinator(ctx, st)
}

// toCoordinator hands the caller to the dialplan's fallback, which rings a
// coordinator; if even that fails, the call ends.
func (b *Bridge) toCoordinator(ctx context.Context, st *bridgeCall) {
	st.done = true
	b.disarm(st)
	octx, cancel := b.op(context.WithoutCancel(ctx))
	defer cancel()
	b.cut(octx, st)
	if err := b.c.ARI.Continue(octx, st.inbound, FallbackContext, "s", 1); err != nil {
		b.c.Log.Error("bridge hand-over to a coordinator failed; hanging up", "err", err)
		b.hangup(ctx, st.inbound)
	}
}

// teardown logs the result and destroys the bridge.
func (b *Bridge) teardown(ctx context.Context, st *bridgeCall, result string) {
	octx, cancel := b.op(context.WithoutCancel(ctx))
	defer cancel()
	if st.id != (uuid.UUID{}) {
		if err := b.c.Store.FinishBridgeCall(octx, st.id, result); err != nil {
			b.c.Log.Error("bridge call end not logged", "bridge_call", st.id, "err", err)
		}
		delete(b.byID, st.id)
	}
	if st.leg != "" {
		delete(b.calls, st.leg)
	}
	if st.bridgeID != "" {
		_ = b.c.ARI.DestroyBridge(octx, st.bridgeID)
	}
}

// finishWith tells the caller why and ends the call once heard.
func (b *Bridge) finishWith(ctx context.Context, st *bridgeCall, sound string) {
	st.done = true
	b.closing(ctx, st, sound)
}

func (b *Bridge) closing(ctx context.Context, st *bridgeCall, sound string) {
	octx, cancel := b.op(ctx)
	defer cancel()
	b.cut(octx, st)
	st.plays++
	st.hangupOn = endAfter(octx, b.c.ARI, st.inbound, fmt.Sprintf("%s-b%d", st.inbound, st.plays), []string{b.sound(sound)})
	st.current = st.hangupOn
	if st.hangupOn != "" {
		// The hang-up follows the prompt's end event; should that be lost,
		// the call still ends.
		b.arm(ctx, st, b.c.PromptMax+b.c.NoInput)
	}
}

// play plays media to the calling party: the prompt a key cuts.
func (b *Bridge) play(ctx context.Context, st *bridgeCall, media ...string) error {
	octx, cancel := b.op(ctx)
	defer cancel()
	st.plays++
	pb := fmt.Sprintf("%s-b%d", st.inbound, st.plays)
	if err := b.c.ARI.Play(octx, st.inbound, pb, media); err != nil {
		return err
	}
	st.current = pb
	return nil
}

func (b *Bridge) cut(ctx context.Context, st *bridgeCall) { cut(ctx, b.c.ARI, &st.current) }

func (b *Bridge) hangup(ctx context.Context, ch string) {
	octx, cancel := b.op(context.WithoutCancel(ctx))
	defer cancel()
	_ = b.c.ARI.Hangup(octx, ch)
}

func (b *Bridge) sound(name string) string { return "sauti/" + b.c.Language + "/" + name }

// BridgeSounds lists the bridge's recordings, for the prompt catalogue.
func BridgeSounds(langs []string) []string {
	var out []string
	for _, lang := range langs {
		for _, s := range []string{"bridge-connecting", "bridge-no-assignment", "bridge-last-four", "bridge-last-four-again",
			"bridge-choose-role", "bridge-choose-side", "bridge-no-answer"} {
			out = append(out, "sauti/"+lang+"/"+s)
		}
	}
	return out
}
