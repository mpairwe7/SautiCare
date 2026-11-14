package ivr

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
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
	Bridge      *Bridge // the masked bridge's calls (bridge, bridge-leg)
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
	if o.Bridge != nil {
		byKind["bridge"], byKind["bridge-leg"] = o.Bridge, o.Bridge
	}
	if e.Type == EventStasisStart {
		if len(e.Args) > 0 {
			if h, ok := byKind[e.Args[0]]; ok {
				h.Handle(ctx, e)
				return
			}
		}
		if o.ARI == nil {
			return
		}
		// The bridge number with no bridge in this process (no caller ID or
		// dial template configured): a coordinator answers (rule 14).
		if c, ok := o.ARI.(interface {
			Continue(ctx context.Context, channelID, dialContext, extension string, priority int) error
		}); ok && len(e.Args) > 0 && e.Args[0] == "bridge" {
			if err := c.Continue(ctx, e.ChannelID, FallbackContext, "s", 1); err == nil {
				return
			}
		}
		_ = o.ARI.Hangup(ctx, e.ChannelID) // not a call Sauti placed: end it
		return
	}
	// Every other event goes to each kind once (the bridge serves two kinds).
	for _, h := range []outboundHandler{o.Alerts, o.Forwards, o.Coordinator, o.Bridge} {
		if h != nil && !reflect.ValueOf(h).IsNil() {
			h.Handle(ctx, e)
		}
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
		current := st.current == e.PlaybackID
		if current {
			st.current = ""
		}
		if (st.hangupOn != "" && st.hangupOn == e.PlaybackID) || (current && failed(e)) {
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

func (f *ForwardCalls) hangup(ctx context.Context, ch string) { _ = hangupFresh(ctx, f.c.ARI, ch) }

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
	// BackupEndpoint is tier 2 (SAUTI_COORDINATOR_BACKUP_DIAL): rung as well
	// once an escalation has rung BackupAfterCalls times unanswered
	// (escalation.backup_after_calls; default 3). "" is no tier 2.
	BackupEndpoint   string
	BackupAfterCalls int
}

// ringMargin is how long after the ring time an unanswered call still holds
// its line (the far end may answer at the last moment).
const ringMargin = 10 * time.Second

type coordinatorCall struct {
	incident  uuid.UUID
	seq       int64
	pairs     []pair // what the call announces, most urgent first
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
	// rounds holds each incident's tier-2 rounds still live (the line and
	// the backup rung together), by the seq they announce, so an
	// acknowledgement hangs up only the calls it covers, and a call placed
	// after it is hung up at once.
	// shortcut: rounds never acknowledged stay until the process restarts
	// (at most maxRounds per incident); prune by age if tier 2 rings often.
	rounds map[uuid.UUID][]*tierRound
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
	if c.BackupAfterCalls <= 0 {
		c.BackupAfterCalls = 3
	}
	return &CoordinatorCalls{c: c, calls: map[string]*coordinatorCall{}, busy: map[string]time.Time{}, rounds: map[uuid.UUID][]*tierRound{}}, nil
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
		args := []string{"escalation", d.IncidentID.String(), strconv.FormatInt(d.Seq, 10), reasonPairs(d.Reasons, d.Roles), string(d.Reference)}
		// Tier 2: after BackupAfterCalls unanswered rings the backup rings
		// too, from the next ring on (Calls counts the ring being placed). It
		// takes none of the line's calls.
		endpoints := []string{k.c.Endpoint}
		if k.c.BackupEndpoint != "" && d.Calls > k.c.BackupAfterCalls {
			endpoints = append(endpoints, k.c.BackupEndpoint)
		}
		var round *tierRound
		if len(endpoints) > 1 {
			round = k.beginRound(d.IncidentID, d.Seq)
		}
		for i, endpoint := range endpoints {
			if round != nil && round.isAcked(&k.mu) {
				break // acknowledged on an earlier call of this round
			}
			legArgs := args
			if i > 0 {
				legArgs = append(slices.Clone(args), backupArg)
			}
			ch, err := bounded(ctx, originateTimeout, func(c context.Context) (string, error) {
				return k.c.ARI.Originate(c, endpoint, k.c.CallerID, legArgs, k.c.Ring)
			})
			if err != nil {
				// The claim stands: the line is rung again after the retry interval.
				k.c.Log.Error("coordinator call not placed", "incident", d.IncidentID, "calls", d.Calls, "tier", i+1, "err", err)
				continue
			}
			// The round was acknowledged while this call was placed: hang it
			// up. A call to the line whose hang-up fails is still ringing and
			// keeps its reservation below.
			if round != nil && !k.addLeg(round, ch) && (k.hangUpLeg(ctx, ch) || i > 0) {
				continue
			}
			if i > 0 {
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
	}
	return len(due), nil
}

// backupArg marks a tier-2 call in its arguments.
const backupArg = "backup"

// pair is one thing a coordinator call announces: a reason and the role it
// was recorded for ("" for none).
type pair struct{ reason, role string }

// roleCodes are the roles an escalation may carry (incident.ParseEscalated),
// one character each in the call's arguments.
var roleCodes = map[string]byte{"": '-', "vht": 'v', "transporter": 't'}

// reasonPairs carries the claimed (reason, role) pairs in the call's
// arguments, most urgent first: two characters each, the reason's place in
// the registry (base 32) and the role, so every pair the registry allows
// fits one argument. Free of any text; a reason outside the registry is
// spoken as "other".
func reasonPairs(reasons, roles []string) string {
	type code struct{ rank, role int }
	var codes []code
	for i, r := range reasons {
		role := ""
		if i < len(roles) {
			role = roles[i]
		}
		rc, ok := roleCodes[role]
		if !ok {
			rc = '-' // not a role an escalation may carry: the reason alone
		}
		codes = append(codes, code{incident.ReasonRank(r), strings.IndexByte("-vt", rc)})
	}
	slices.SortFunc(codes, func(a, b code) int { return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.role, b.role)) })
	var b strings.Builder
	for _, c := range slices.Compact(codes) {
		b.WriteString(strconv.FormatInt(int64(c.rank), 32))
		b.WriteByte("-vt"[c.role])
	}
	return b.String()
}

// pairsOf reads reasonPairs back; anything else is spoken as "other".
func pairsOf(arg string) []pair {
	var out []pair
	for i := 0; i+1 < len(arg); i += 2 {
		p := pair{reason: "other"}
		if n, err := strconv.ParseInt(arg[i:i+1], 32, 8); err == nil && int(n) < len(incident.EscalationReasons) {
			p.reason = incident.EscalationReasons[n]
		}
		for role, c := range roleCodes {
			if arg[i+1] == c {
				p.role = role
			}
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
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
		backup := len(e.Args) == 6 && e.Args[5] == backupArg
		if len(e.Args) != 5 && !backup {
			k.hangup(op, e.ChannelID)
			return
		}
		inc, err1 := uuid.Parse(e.Args[1])
		seq, err2 := strconv.ParseInt(e.Args[2], 10, 64)
		if err1 != nil || err2 != nil {
			k.hangup(op, e.ChannelID)
			return
		}
		st := &coordinatorCall{incident: inc, seq: seq, pairs: pairsOf(e.Args[3]), reference: incident.Reference(e.Args[4])}
		k.mu.Lock()
		// Answered: the line is busy until the call ends, whether or not
		// RunOnce has stored the call yet (CodeRabbit PR #11). The backup
		// line is another phone: its call holds none of the line's calls.
		if !backup {
			k.busy[e.ChannelID] = time.Time{}
		}
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
			k.hangUpOtherLegs(op, st.incident, st.seq, e.ChannelID)
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
		current := st.current == e.PlaybackID
		if current {
			st.current = ""
		}
		if (st.hangupOn != "" && st.hangupOn == e.PlaybackID) || (current && failed(e)) {
			k.hangup(op, e.ChannelID)
		}
	case EventStasisEnd, EventHangupRequest:
		k.release(e.ChannelID, nil)
	}
}

// hangUpOtherLegs ends the other tier's call about an acknowledged incident.
func (k *CoordinatorCalls) hangUpOtherLegs(ctx context.Context, inc uuid.UUID, seq int64, acked string) {
	k.mu.Lock()
	var legs []string
	var live []*tierRound
	for _, r := range k.rounds[inc] {
		if r.seq > seq {
			live = append(live, r) // a newer escalation, not acknowledged
			continue
		}
		r.acked, r.ackedBy = true, acked
		legs = append(legs, r.legs...)
	}
	k.rounds[inc] = live
	k.mu.Unlock()
	for _, ch := range legs {
		if ch != acked {
			k.hangUpLeg(ctx, ch)
		}
	}
}

// tierRound is one tier-2 round: the calls placed for one claimed seq.
type tierRound struct {
	seq     int64
	legs    []string
	acked   bool
	ackedBy string // the call that acknowledged it: never hung up for it
}

// maxRounds bounds the rounds kept per incident.
const maxRounds = 4

func (k *CoordinatorCalls) beginRound(inc uuid.UUID, seq int64) *tierRound {
	k.mu.Lock()
	defer k.mu.Unlock()
	r := &tierRound{seq: seq}
	k.rounds[inc] = append(k.rounds[inc], r)
	if n := len(k.rounds[inc]); n > maxRounds {
		k.rounds[inc] = k.rounds[inc][n-maxRounds:]
	}
	return r
}

func (r *tierRound) isAcked(mu *sync.Mutex) bool {
	mu.Lock()
	defer mu.Unlock()
	return r.acked
}

// addLeg records a call placed for the round. It reports false for a call
// to hang up: the round was acknowledged meanwhile, on another call.
func (k *CoordinatorCalls) addLeg(r *tierRound, ch string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if r.acked {
		return ch == r.ackedBy
	}
	r.legs = append(r.legs, ch)
	return true
}

// hangUpLeg ends another call of an acknowledged round and reports whether
// Asterisk took the hang-up. Its line is freed only then; a call still live
// keeps its line and its no-input time.
func (k *CoordinatorCalls) hangUpLeg(ctx context.Context, ch string) bool {
	if _, err := bounded(context.WithoutCancel(ctx), k.c.OpTimeout, func(c context.Context) (struct{}, error) { return struct{}{}, k.c.ARI.Hangup(c, ch) }); err != nil {
		k.c.Log.Error("hanging up the other coordinator call failed; its line stays held", "err", err)
		return false
	}
	k.release(ch, nil)
	return true
}

func (k *CoordinatorCalls) sounds(st *coordinatorCall) []string {
	b := "sauti/" + k.c.Language + "/"
	out := append([]string{b + "escalation-intro"}, digits(st.reference)...)
	for _, p := range st.pairs {
		out = append(out, escalationReasonSound(k.c.Language, p.reason))
		if p.role != "" {
			out = append(out, b+"role-"+p.role) // which role nobody can reach
		}
	}
	return append(out, b+"escalation-options")
}

func (k *CoordinatorCalls) play(ctx context.Context, ch string, st *coordinatorCall, sounds []string) {
	st.plays++
	pb := fmt.Sprintf("%s-c%d", ch, st.plays)
	if k.c.ARI.Play(ctx, ch, pb, sounds) == nil {
		st.current = pb
	}
}

func (k *CoordinatorCalls) hangup(ctx context.Context, ch string) { _ = hangupFresh(ctx, k.c.ARI, ch) }

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
		_ = hangupFresh(ctx, a, ch)
		return ""
	}
	return pb
}

// hangupTimeout bounds a hang-up made on a deadline of its own.
const hangupTimeout = 3 * time.Second

// hangupFresh hangs up on a deadline of its own: the operation before it
// (a playback or a store call) may have used up ctx's, and a refused
// hang-up would leave the line open.
func hangupFresh(ctx context.Context, a AlertARI, ch string) error {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hangupTimeout)
	defer cancel()
	return a.Hangup(hctx, ch)
}

// outboundKey reports whether a forwarding or coordinator call takes digit:
// 1 acknowledges, 3 repeats. Any other key is ignored, leaving the prompt
// playing rather than cutting it to silence.
func outboundKey(digit string) bool { return digit == "1" || digit == "3" }

// failed reports a prompt Asterisk could not play (F-03). On an outbound
// call nobody heard what to do: the call ends unanswered, and the alert,
// forward or escalation goes on as for a call that was not picked up (its
// attempt was settled when the call was placed).
func failed(e Event) bool { return e.PlaybackState == playbackFailed }

// cut stops the prompt being heard when a key the menu offers is pressed, so
// the answer is spoken at once rather than queued behind it.
func cut(ctx context.Context, a AlertARI, current *string) {
	if *current != "" {
		_ = a.StopPlayback(ctx, *current)
		*current = ""
	}
}

// escalationReasonSound is the reason's recording; a reason outside the
// registry is spoken as "other".
func escalationReasonSound(lang, reason string) string {
	if !slices.Contains(incident.EscalationReasons, reason) {
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
			b+"escalation-intro", b+"escalation-options", b+"escalation-acknowledged", b+"escalation-reason-other",
			b+"role-vht", b+"role-transporter")
		for _, r := range incident.EscalationReasons {
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
