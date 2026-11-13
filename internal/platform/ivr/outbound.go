package ivr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

// Forwarding calls and coordinator escalation calls (M2 Part B Task 11;
// Build Prompt §10.4; scenarios 7, 8, 11). Like alert calls they run on the
// outbound Stasis application, record nothing, and speak recorded prompts.

// Outbound routes the outbound application's events: an answered call to
// its kind (its first application argument), every other event to all.
type Outbound struct {
	ARI         AlertARI
	Alerts      *AlertCalls
	Forwards    *ForwardCalls
	Coordinator *CoordinatorCalls
}

type outboundHandler interface {
	Handle(ctx context.Context, e Event)
}

// Handle routes one event; an answered call of no known kind is hung up.
func (o Outbound) Handle(ctx context.Context, e Event) {
	byKind := map[string]outboundHandler{}
	if o.Alerts != nil {
		byKind["alert"] = o.Alerts
	}
	if o.Forwards != nil {
		byKind["forward"] = o.Forwards
	}
	if o.Coordinator != nil {
		byKind["escalation"] = o.Coordinator
	}
	if e.Type == EventStasisStart {
		if len(e.Args) > 0 {
			if h, ok := byKind[e.Args[0]]; ok {
				h.Handle(ctx, e)
				return
			}
		}
		if o.ARI != nil { // not a call Sauti placed: end it
			_ = o.ARI.Hangup(ctx, e.ChannelID)
		}
		return
	}
	for _, h := range byKind {
		h.Handle(ctx, e)
	}
}

// ForwardStore is the store as forwarding calls see it (op_ivr).
type ForwardStore interface {
	DueForwardCalls(ctx context.Context, limit int) ([]dispatch.ForwardCall, error)
	BeginForwardAttempt(ctx context.Context, incidentID uuid.UUID, facility, channel string) (uuid.UUID, bool, error)
	FinishForwardAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error)
	MessageFacts(ctx context.Context, incidentID uuid.UUID) (dispatch.MessageFacts, error)
	AcknowledgeForward(ctx context.Context, incidentID uuid.UUID, facility string) (bool, error)
}

// ForwardCallsConfig wires forwarding calls.
type ForwardCallsConfig struct {
	ARI       AlertARI
	Store     ForwardStore
	Dial      string // endpoint template with {number}
	CallerID  string
	Gazetteer location.File // facilities' public numbers
	Ring      time.Duration
	Language  string
	Log       *slog.Logger
	OpTimeout time.Duration
	NoInput   time.Duration // an answered call with no key it offers for this long is hung up (default DefaultNoInput)
}

type forwardCall struct {
	incident uuid.UUID
	facility string
	facts    dispatch.MessageFacts
	plays    int
	done     bool
	current  string
	hangupOn string
	timer    *time.Timer // the no-input time (set under ForwardCalls.mu)
}

// ForwardCalls calls forwarded health centres: press 1 to acknowledge.
type ForwardCalls struct {
	c     ForwardCallsConfig
	mu    sync.Mutex
	calls map[string]*forwardCall
}

// NewForwardCalls validates the wiring.
func NewForwardCalls(c ForwardCallsConfig) (*ForwardCalls, error) {
	if c.ARI == nil || c.Store == nil || !strings.Contains(c.Dial, "{number}") {
		return nil, errors.New("ivr: forwarding calls need ARI, a store and a dial template with {number}")
	}
	c.Ring, c.Language, c.Log, c.OpTimeout = defaults(c.Ring, c.Language, c.Log, c.OpTimeout)
	if c.NoInput <= 0 {
		c.NoInput = DefaultNoInput
	}
	return &ForwardCalls{c: c, calls: map[string]*forwardCall{}}, nil
}

// DefaultNoInput is how long an answered forwarding or coordinator call may
// go without a key it offers before Sauti hangs up (CodeRabbit PR #11): an
// unattended answer cannot hold a coordinator line (or a channel) for ever.
// The escalation, still unacknowledged, rings again at its retry interval.
const DefaultNoInput = 2 * time.Minute

func defaults(ring time.Duration, lang string, log *slog.Logger, op time.Duration) (time.Duration, string, *slog.Logger, time.Duration) {
	if ring <= 0 {
		ring = 30 * time.Second
	}
	if lang == "" {
		lang = "en"
	}
	if log == nil {
		log = slog.Default()
	}
	if op <= 0 {
		op = 3 * time.Second
	}
	return ring, lang, log, op
}

// RunOnce calls every forwarded facility not yet called, once each.
func (f *ForwardCalls) RunOnce(ctx context.Context) (int, error) {
	due, err := bounded(ctx, f.c.OpTimeout, func(c context.Context) ([]dispatch.ForwardCall, error) {
		return f.c.Store.DueForwardCalls(c, 50)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		claim, err := bounded(ctx, f.c.OpTimeout, func(c context.Context) (claimed, error) {
			id, fresh, err := f.c.Store.BeginForwardAttempt(c, d.IncidentID, d.FacilityID, dispatch.ChannelVoice)
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
		ref, cerr := bounded(ctx, originateTimeout, func(c context.Context) (string, error) { return f.dial(c, d) })
		status := "sent"
		if cerr != nil {
			status = "unknown"
			if errors.Is(cerr, dispatch.ErrRejected) {
				status = "failed"
			}
			f.c.Log.Error("forwarding call not placed", "incident", d.IncidentID, "facility", d.FacilityID, "err", cerr)
		}
		if _, err := bounded(context.WithoutCancel(ctx), f.c.OpTimeout, func(c context.Context) (bool, error) {
			return f.c.Store.FinishForwardAttempt(c, attempt, status, ref)
		}); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (f *ForwardCalls) dial(ctx context.Context, d dispatch.ForwardCall) (string, error) {
	for _, fac := range f.c.Gazetteer.Facilities {
		if fac.ID != d.FacilityID {
			continue
		}
		p, err := pii.ParsePhone(fac.Phone)
		if err != nil {
			return "", fmt.Errorf("%w: facility %s has no usable number", dispatch.ErrRejected, fac.ID)
		}
		return f.c.ARI.Originate(ctx, strings.ReplaceAll(f.c.Dial, "{number}", p.E164()), f.c.CallerID,
			[]string{"forward", d.IncidentID.String(), d.FacilityID}, f.c.Ring)
	}
	return "", fmt.Errorf("%w: facility %q is not in the gazetteer in force", dispatch.ErrRejected, d.FacilityID)
}

// Run calls forwarded facilities every interval until ctx ends.
func (f *ForwardCalls) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	return every_(ctx, every, func() error { _, err := f.RunOnce(ctx); return err }, onErr)
}

// Handle runs answered forwarding calls.
func (f *ForwardCalls) Handle(ctx context.Context, e Event) {
	op, cancel := context.WithTimeout(ctx, f.c.OpTimeout)
	defer cancel()
	switch e.Type {
	case EventStasisStart:
		if len(e.Args) != 3 {
			f.hangup(op, e.ChannelID)
			return
		}
		inc, err := uuid.Parse(e.Args[1])
		if err != nil {
			f.hangup(op, e.ChannelID)
			return
		}
		facts, err := f.c.Store.MessageFacts(op, inc)
		if err != nil {
			f.hangup(op, e.ChannelID)
			return
		}
		st := &forwardCall{incident: inc, facility: e.Args[2], facts: facts}
		f.mu.Lock()
		f.calls[e.ChannelID] = st
		f.mu.Unlock()
		f.arm(ctx, e.ChannelID, st) // the no-input time runs from the answer
		if err := f.c.ARI.Answer(op, e.ChannelID); err == nil {
			f.play(op, e.ChannelID, st, f.sounds(facts))
		}
	case EventDTMF:
		f.mu.Lock()
		st := f.calls[e.ChannelID]
		f.mu.Unlock()
		if st == nil || st.done || !outboundKey(e.Digit) {
			return
		}
		// A key the call offers is input: the no-input time starts again (after
		// an acknowledgement it backs up the hang-up after the closing prompt).
		f.arm(ctx, e.ChannelID, st)
		cut(op, f.c.ARI, &st.current)
		switch e.Digit {
		case "1":
			if _, err := f.c.Store.AcknowledgeForward(op, st.incident, st.facility); err != nil {
				f.c.Log.Error("forward acknowledgement failed", "incident", st.incident, "err", err)
				return
			}
			st.done = true
			st.plays++
			st.hangupOn = endAfter(op, f.c.ARI, e.ChannelID, fmt.Sprintf("%s-f%d", e.ChannelID, st.plays),
				[]string{"sauti/" + f.c.Language + "/forward-acknowledged"})
			st.current = st.hangupOn
		case "3":
			f.play(op, e.ChannelID, st, f.sounds(st.facts))
		}
	case EventPlaybackFinished:
		f.mu.Lock()
		st := f.calls[e.ChannelID]
		f.mu.Unlock()
		if st == nil {
			return
		}
		if st.current == e.PlaybackID {
			st.current = ""
		}
		if st.hangupOn != "" && st.hangupOn == e.PlaybackID {
			f.hangup(op, e.ChannelID)
		}
	case EventStasisEnd, EventHangupRequest:
		f.mu.Lock()
		if st := f.calls[e.ChannelID]; st != nil && st.timer != nil {
			st.timer.Stop()
		}
		delete(f.calls, e.ChannelID)
		f.mu.Unlock()
	}
}

// arm starts the call's no-input time, or starts it again. The timer runs on
// its own goroutine, so it reads the call state under f.mu.
func (f *ForwardCalls) arm(ctx context.Context, ch string, st *forwardCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st.timer != nil {
		st.timer.Reset(f.c.NoInput)
		return
	}
	bg := context.WithoutCancel(ctx) // the hang-up outlives the event and the term
	st.timer = time.AfterFunc(f.c.NoInput, func() { f.noInput(bg, ch, st) })
}

// noInput hangs up an answered forwarding call that heard no key it offers
// for the no-input time.
func (f *ForwardCalls) noInput(ctx context.Context, ch string, st *forwardCall) {
	f.mu.Lock()
	live := f.calls[ch] == st
	if live {
		delete(f.calls, ch) // a forwarding call holds no line: nothing else to free
	}
	f.mu.Unlock()
	if !live {
		return
	}
	f.c.Log.Warn("forwarding call hung up after no input", "incident", st.incident, "facility", st.facility)
	if _, err := bounded(ctx, f.c.OpTimeout, func(c context.Context) (struct{}, error) { return struct{}{}, f.c.ARI.Hangup(c, ch) }); err != nil {
		f.c.Log.Error("hanging up a forwarding call with no input failed", "incident", st.incident, "err", err)
	}
}

func (f *ForwardCalls) sounds(facts dispatch.MessageFacts) []string {
	b := "sauti/" + f.c.Language + "/"
	out := append([]string{b + "forward-intro"}, digits(facts.Reference)...)
	out = append(out, b+"alert-colour-"+string(facts.Severity))
	if facts.Category != "" {
		out = append(out, b+"alert-category-"+facts.Category)
	}
	return append(out, b+"forward-options")
}

func (f *ForwardCalls) play(ctx context.Context, ch string, st *forwardCall, sounds []string) {
	st.plays++
	pb := fmt.Sprintf("%s-f%d", ch, st.plays)
	if f.c.ARI.Play(ctx, ch, pb, sounds) == nil {
		st.current = pb
	}
}

func (f *ForwardCalls) hangup(ctx context.Context, ch string) { _ = f.c.ARI.Hangup(ctx, ch) }

// CoordinatorStore is the store as coordinator calls see it (op_ivr).
type CoordinatorStore interface {
	ClaimCoordinatorCalls(ctx context.Context, retry time.Duration, limit int) ([]dispatch.CoordinatorCall, error)
	AcknowledgeCoordinatorCall(ctx context.Context, incidentID uuid.UUID, seq int64) error
}

// CoordinatorARI is what coordinator calls need from Asterisk: alert calls'
// operations, and whether a channel still exists (a new term checks the lines
// it holds).
type CoordinatorARI interface {
	AlertARI
	ChannelExists(ctx context.Context, channelID string) (bool, error)
}

// CoordinatorCallsConfig wires coordinator escalation calls.
type CoordinatorCallsConfig struct {
	ARI       CoordinatorARI
	Store     CoordinatorStore
	Endpoint  string // the coordinator line (SAUTI_COORDINATOR_DIAL; a placeholder until P0)
	CallerID  string
	Retry     time.Duration // ring again until acknowledged (default 2 min)
	Ring      time.Duration
	Lines     int              // calls the coordinator line takes at once (SAUTI_COORDINATOR_LINES; default 1)
	Now       func() time.Time // the clock (default time.Now)
	Language  string
	Log       *slog.Logger
	OpTimeout time.Duration
	NoInput   time.Duration // an answered call with no key it offers for this long is hung up (default DefaultNoInput)
}

// ringMargin is how long after the ring time an unanswered call still holds
// its line (the far end may answer at the last moment).
const ringMargin = 10 * time.Second

type coordinatorCall struct {
	incident  uuid.UUID
	seq       int64
	reason    string
	reference incident.Reference
	plays     int
	done      bool
	current   string
	hangupOn  string
	timer     *time.Timer // the no-input time (set under CoordinatorCalls.mu)
}

// CoordinatorCalls rings the coordinator line about every escalation,
// durable and retried until a coordinator presses 1 (rule 3.14).
type CoordinatorCalls struct {
	c     CoordinatorCallsConfig
	mu    sync.Mutex
	calls map[string]*coordinatorCall
	// busy holds a line per call placed: until the ring time has passed
	// unanswered, or while it is answered (zero time) until it ends — its
	// StasisEnd, a hang-up after no input, or a new term finding the channel
	// gone (reconcile).
	busy map[string]time.Time
}

// NewCoordinatorCalls validates the wiring.
func NewCoordinatorCalls(c CoordinatorCallsConfig) (*CoordinatorCalls, error) {
	if c.ARI == nil || c.Store == nil || c.Endpoint == "" {
		return nil, errors.New("ivr: coordinator calls need ARI, a store and the coordinator line")
	}
	if c.Retry <= 0 {
		c.Retry = 2 * time.Minute
	}
	if c.Lines <= 0 {
		c.Lines = 1
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	c.Ring, c.Language, c.Log, c.OpTimeout = defaults(c.Ring, c.Language, c.Log, c.OpTimeout)
	if c.NoInput <= 0 {
		c.NoInput = DefaultNoInput
	}
	return &CoordinatorCalls{c: c, calls: map[string]*coordinatorCall{}, busy: map[string]time.Time{}}, nil
}

// RunOnce rings the coordinator line about each escalation due now.
// Only as many escalations are claimed as the line has free: a line is
// never rung twice at once, and the most severe escalation goes first.
func (k *CoordinatorCalls) RunOnce(ctx context.Context) (int, error) {
	now := k.c.Now()
	k.mu.Lock()
	for ch, until := range k.busy {
		if !until.IsZero() && now.After(until) {
			delete(k.busy, ch)
		}
	}
	free := k.c.Lines - len(k.busy)
	k.mu.Unlock()
	if free <= 0 {
		return 0, nil
	}
	due, err := bounded(ctx, k.c.OpTimeout, func(c context.Context) ([]dispatch.CoordinatorCall, error) {
		return k.c.Store.ClaimCoordinatorCalls(c, k.c.Retry, free)
	})
	if err != nil {
		return 0, err
	}
	for _, d := range due {
		reason := d.Reason
		if !reasonCode.MatchString(reason) {
			reason = "other"
		}
		ch, err := bounded(ctx, originateTimeout, func(c context.Context) (string, error) {
			return k.c.ARI.Originate(c, k.c.Endpoint, k.c.CallerID,
				[]string{"escalation", d.IncidentID.String(), strconv.FormatInt(d.Seq, 10), reason, string(d.Reference)}, k.c.Ring)
		})
		if err != nil {
			// The claim stands: the line is rung again after the retry interval.
			k.c.Log.Error("coordinator call not placed", "incident", d.IncidentID, "calls", d.Calls, "err", err)
			continue
		}
		k.mu.Lock()
		// The answer may already have been handled (StasisStart can arrive
		// before Originate returns): its mark stands, or the reservation
		// would lapse while the call is under way.
		if until, answered := k.busy[ch]; !answered || !until.IsZero() {
			k.busy[ch] = now.Add(k.c.Ring + ringMargin)
		}
		k.mu.Unlock()
	}
	return len(due), nil
}

// Run rings due escalations every interval until ctx ends. It first frees
// the lines of calls that ended unheard (reconcile): Run starts with every
// leadership term and every reconnection of the event stream, exactly when
// end events may have been missed.
func (k *CoordinatorCalls) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	k.reconcile(ctx)
	return every_(ctx, every, func() error { _, err := k.RunOnce(ctx); return err }, onErr)
}

// reconcile checks every line this process holds against Asterisk. A call's
// StasisEnd goes only to the process holding the outbound websocket, so a
// call that ended while another process led, or while the stream was down,
// would hold its line for ever (with one line, no escalation would ring
// again). A channel Asterisk reports gone (404) frees its line; one that
// still exists keeps it, and so does one Asterisk cannot be asked about: a
// line in use is never rung twice.
func (k *CoordinatorCalls) reconcile(ctx context.Context) {
	k.mu.Lock()
	held := slices.Collect(maps.Keys(k.busy))
	k.mu.Unlock()
	for _, ch := range held {
		live, err := bounded(ctx, k.c.OpTimeout, func(c context.Context) (bool, error) { return k.c.ARI.ChannelExists(c, ch) })
		switch {
		case err != nil:
			if ctx.Err() == nil {
				k.c.Log.Error("coordinator line not checked against Asterisk; kept", "err", err)
			}
		case !live:
			k.release(ch, nil)
		}
	}
}

// release frees a call's line and forgets the call, stopping its no-input
// time; with st given, only while ch is still that call.
func (k *CoordinatorCalls) release(ch string, st *coordinatorCall) {
	k.mu.Lock()
	defer k.mu.Unlock()
	cur := k.calls[ch]
	if st != nil && cur != st {
		return
	}
	if cur != nil && cur.timer != nil {
		cur.timer.Stop()
	}
	delete(k.calls, ch)
	delete(k.busy, ch)
}

// arm starts the call's no-input time, or starts it again. The timer runs on
// its own goroutine, so it reads the call state under k.mu.
func (k *CoordinatorCalls) arm(ctx context.Context, ch string, st *coordinatorCall) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if st.timer != nil {
		st.timer.Reset(k.c.NoInput)
		return
	}
	bg := context.WithoutCancel(ctx) // the hang-up outlives the event and the term
	st.timer = time.AfterFunc(k.c.NoInput, func() { k.noInput(bg, ch, st) })
}

// noInput hangs up an answered coordinator call that heard no key it offers
// for the no-input time. Once Asterisk has taken the hang-up the line is
// free at once (the StasisEnd may never reach this process); the
// escalation, unacknowledged, rings again at its retry interval. A hang-up
// that fails leaves the line held until the call's end or the next term's
// reconcile.
func (k *CoordinatorCalls) noInput(ctx context.Context, ch string, st *coordinatorCall) {
	k.mu.Lock()
	live := k.calls[ch] == st
	k.mu.Unlock()
	if !live {
		return
	}
	k.c.Log.Warn("coordinator call hung up after no input", "incident", st.incident)
	if _, err := bounded(ctx, k.c.OpTimeout, func(c context.Context) (struct{}, error) { return struct{}{}, k.c.ARI.Hangup(c, ch) }); err != nil {
		k.c.Log.Error("hanging up a coordinator call with no input failed", "incident", st.incident, "err", err)
		return
	}
	k.release(ch, st)
}

// Handle runs answered coordinator calls.
func (k *CoordinatorCalls) Handle(ctx context.Context, e Event) {
	op, cancel := context.WithTimeout(ctx, k.c.OpTimeout)
	defer cancel()
	switch e.Type {
	case EventStasisStart:
		if len(e.Args) != 5 {
			k.hangup(op, e.ChannelID)
			return
		}
		inc, err1 := uuid.Parse(e.Args[1])
		seq, err2 := strconv.ParseInt(e.Args[2], 10, 64)
		if err1 != nil || err2 != nil {
			k.hangup(op, e.ChannelID)
			return
		}
		st := &coordinatorCall{incident: inc, seq: seq, reason: e.Args[3], reference: incident.Reference(e.Args[4])}
		k.mu.Lock()
		// Answered: the line is busy until the call ends, whether or not
		// RunOnce has stored the call yet (CodeRabbit PR #11).
		k.busy[e.ChannelID] = time.Time{}
		k.calls[e.ChannelID] = st
		k.mu.Unlock()
		k.arm(ctx, e.ChannelID, st) // the no-input time runs from the answer
		if err := k.c.ARI.Answer(op, e.ChannelID); err == nil {
			k.play(op, e.ChannelID, st, k.sounds(st))
		}
	case EventDTMF:
		k.mu.Lock()
		st := k.calls[e.ChannelID]
		k.mu.Unlock()
		if st == nil || st.done || !outboundKey(e.Digit) {
			return
		}
		// A key the call offers is input: the no-input time starts again (after
		// an acknowledgement it backs up the hang-up after the closing prompt).
		k.arm(ctx, e.ChannelID, st)
		cut(op, k.c.ARI, &st.current)
		switch e.Digit {
		case "1":
			if err := k.c.Store.AcknowledgeCoordinatorCall(op, st.incident, st.seq); err != nil {
				k.c.Log.Error("coordinator acknowledgement failed", "incident", st.incident, "err", err)
				return
			}
			st.done = true
			st.plays++
			st.hangupOn = endAfter(op, k.c.ARI, e.ChannelID, fmt.Sprintf("%s-c%d", e.ChannelID, st.plays),
				[]string{"sauti/" + k.c.Language + "/escalation-acknowledged"})
			st.current = st.hangupOn
		case "3":
			k.play(op, e.ChannelID, st, k.sounds(st))
		}
	case EventPlaybackFinished:
		k.mu.Lock()
		st := k.calls[e.ChannelID]
		k.mu.Unlock()
		if st == nil {
			return
		}
		if st.current == e.PlaybackID {
			st.current = ""
		}
		if st.hangupOn != "" && st.hangupOn == e.PlaybackID {
			k.hangup(op, e.ChannelID)
		}
	case EventStasisEnd, EventHangupRequest:
		k.release(e.ChannelID, nil)
	}
}

func (k *CoordinatorCalls) sounds(st *coordinatorCall) []string {
	b := "sauti/" + k.c.Language + "/"
	out := append([]string{b + "escalation-intro"}, digits(st.reference)...)
	return append(out, escalationReasonSound(k.c.Language, st.reason), b+"escalation-options")
}

func (k *CoordinatorCalls) play(ctx context.Context, ch string, st *coordinatorCall, sounds []string) {
	st.plays++
	pb := fmt.Sprintf("%s-c%d", ch, st.plays)
	if k.c.ARI.Play(ctx, ch, pb, sounds) == nil {
		st.current = pb
	}
}

func (k *CoordinatorCalls) hangup(ctx context.Context, ch string) { _ = k.c.ARI.Hangup(ctx, ch) }

// originateTimeout bounds placing an outbound call (Asterisk creates the
// channel and answers at once; the ring itself is not waited for).
const originateTimeout = 10 * time.Second

// claimed is an attempt claim's result.
type claimed struct {
	id    uuid.UUID
	fresh bool
}

// bounded runs one store or ARI operation under a deadline of its own: the
// leader's context brings cancellation, never a deadline, so a database or
// Asterisk that hangs would otherwise stall the worker for the whole term.
func bounded[T any](ctx context.Context, d time.Duration, f func(context.Context) (T, error)) (T, error) {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return f(c)
}

// endAfter plays a closing prompt and returns its playback ID, to hang up on
// once it has been heard (ARI plays asynchronously: hanging up at once would
// cut it off); a prompt that cannot be played ends the call at once.
func endAfter(ctx context.Context, a AlertARI, ch, pb string, sounds []string) string {
	if err := a.Play(ctx, ch, pb, sounds); err != nil {
		_ = a.Hangup(ctx, ch)
		return ""
	}
	return pb
}

// outboundKey reports whether a forwarding or coordinator call takes digit:
// 1 acknowledges, 3 repeats. Any other key is ignored, leaving the prompt
// playing rather than cutting it to silence.
func outboundKey(digit string) bool { return digit == "1" || digit == "3" }

// cut stops the prompt being heard when a key the menu offers is pressed, so
// the answer is spoken at once rather than queued behind it.
func cut(ctx context.Context, a AlertARI, current *string) {
	if *current != "" {
		_ = a.StopPlayback(ctx, *current)
		*current = ""
	}
}

var reasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// EscalationReasons are the reasons with their own recording; any other is
// spoken as "other".
var EscalationReasons = []string{
	"triage_rule_error", "abandoned_after_danger_yes", "abandoned_red_before_dispatch", "merged_call_escalated",
	"no_eligible_responder", "no_responder_nearby", "red_no_acceptance", "forwarded_to_facility",
	"no_facility_acknowledged", "dispatch_suspended", "dispatch_error", "dispatch_stalled",
}

func escalationReasonSound(lang, reason string) string {
	if !slices.Contains(EscalationReasons, reason) {
		reason = "other"
	}
	return "sauti/" + lang + "/escalation-reason-" + reason
}

func digits(ref incident.Reference) []string {
	var out []string
	for _, r := range ref.Spoken() {
		if r >= '0' && r <= '9' {
			out = append(out, "sauti/digits/"+string(r))
		}
	}
	return out
}

// OutboundSounds lists the recordings forwarding and coordinator calls play,
// for the prompt catalogue.
func OutboundSounds(copy config.Responder) []string {
	var out []string
	for _, lang := range copy.Languages {
		b := "sauti/" + lang + "/"
		out = append(out, b+"forward-intro", b+"forward-options", b+"forward-acknowledged",
			b+"escalation-intro", b+"escalation-options", b+"escalation-acknowledged", b+"escalation-reason-other")
		for _, r := range EscalationReasons {
			out = append(out, b+"escalation-reason-"+r)
		}
	}
	return out
}

func every_(ctx context.Context, every time.Duration, f func() error, onErr func(error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := f(); err != nil && ctx.Err() == nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
