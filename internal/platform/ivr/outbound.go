package ivr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
}

type forwardCall struct {
	incident uuid.UUID
	facility string
	facts    dispatch.MessageFacts
	plays    int
	done     bool
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
	return &ForwardCalls{c: c, calls: map[string]*forwardCall{}}, nil
}

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
	due, err := f.c.Store.DueForwardCalls(ctx, 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range due {
		attempt, fresh, err := f.c.Store.BeginForwardAttempt(ctx, d.IncidentID, d.FacilityID, dispatch.ChannelVoice)
		if err != nil {
			return n, err
		}
		if !fresh {
			continue
		}
		n++
		ref, cerr := f.dial(ctx, d)
		status := "sent"
		if cerr != nil {
			status = "unknown"
			if errors.Is(cerr, dispatch.ErrRejected) {
				status = "failed"
			}
			f.c.Log.Error("forwarding call not placed", "incident", d.IncidentID, "facility", d.FacilityID, "err", cerr)
		}
		if _, err := f.c.Store.FinishForwardAttempt(ctx, attempt, status, ref); err != nil {
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
		if err := f.c.ARI.Answer(op, e.ChannelID); err == nil {
			f.play(op, e.ChannelID, st, f.sounds(facts))
		}
	case EventDTMF:
		f.mu.Lock()
		st := f.calls[e.ChannelID]
		f.mu.Unlock()
		if st == nil || st.done {
			return
		}
		switch e.Digit {
		case "1":
			if _, err := f.c.Store.AcknowledgeForward(op, st.incident, st.facility); err != nil {
				f.c.Log.Error("forward acknowledgement failed", "incident", st.incident, "err", err)
				return
			}
			st.done = true
			f.play(op, e.ChannelID, st, []string{"sauti/" + f.c.Language + "/forward-acknowledged"})
			f.hangup(op, e.ChannelID)
		case "3":
			f.play(op, e.ChannelID, st, f.sounds(st.facts))
		}
	case EventStasisEnd, EventHangupRequest:
		f.mu.Lock()
		delete(f.calls, e.ChannelID)
		f.mu.Unlock()
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
	_ = f.c.ARI.Play(ctx, ch, fmt.Sprintf("%s-f%d", ch, st.plays), sounds)
}

func (f *ForwardCalls) hangup(ctx context.Context, ch string) { _ = f.c.ARI.Hangup(ctx, ch) }

// CoordinatorStore is the store as coordinator calls see it (op_ivr).
type CoordinatorStore interface {
	ClaimCoordinatorCalls(ctx context.Context, retry time.Duration, limit int) ([]dispatch.CoordinatorCall, error)
	AcknowledgeCoordinatorCall(ctx context.Context, incidentID uuid.UUID, seq int64) error
}

// CoordinatorCallsConfig wires coordinator escalation calls.
type CoordinatorCallsConfig struct {
	ARI       AlertARI
	Store     CoordinatorStore
	Endpoint  string // the coordinator line (SAUTI_COORDINATOR_DIAL; a placeholder until P0)
	CallerID  string
	Retry     time.Duration // ring again until acknowledged (default 2 min)
	Ring      time.Duration
	Language  string
	Log       *slog.Logger
	OpTimeout time.Duration
}

type coordinatorCall struct {
	incident  uuid.UUID
	seq       int64
	reason    string
	reference incident.Reference
	plays     int
	done      bool
}

// CoordinatorCalls rings the coordinator line about every escalation,
// durable and retried until a coordinator presses 1 (rule 3.14).
type CoordinatorCalls struct {
	c     CoordinatorCallsConfig
	mu    sync.Mutex
	calls map[string]*coordinatorCall
}

// NewCoordinatorCalls validates the wiring.
func NewCoordinatorCalls(c CoordinatorCallsConfig) (*CoordinatorCalls, error) {
	if c.ARI == nil || c.Store == nil || c.Endpoint == "" {
		return nil, errors.New("ivr: coordinator calls need ARI, a store and the coordinator line")
	}
	if c.Retry <= 0 {
		c.Retry = 2 * time.Minute
	}
	c.Ring, c.Language, c.Log, c.OpTimeout = defaults(c.Ring, c.Language, c.Log, c.OpTimeout)
	return &CoordinatorCalls{c: c, calls: map[string]*coordinatorCall{}}, nil
}

// RunOnce rings the coordinator line about each escalation due now.
func (k *CoordinatorCalls) RunOnce(ctx context.Context) (int, error) {
	due, err := k.c.Store.ClaimCoordinatorCalls(ctx, k.c.Retry, 20)
	if err != nil {
		return 0, err
	}
	for _, d := range due {
		reason := d.Reason
		if !reasonCode.MatchString(reason) {
			reason = "other"
		}
		if _, err := k.c.ARI.Originate(ctx, k.c.Endpoint, k.c.CallerID,
			[]string{"escalation", d.IncidentID.String(), strconv.FormatInt(d.Seq, 10), reason, string(d.Reference)}, k.c.Ring); err != nil {
			// The claim stands: the line is rung again after the retry interval.
			k.c.Log.Error("coordinator call not placed", "incident", d.IncidentID, "calls", d.Calls, "err", err)
		}
	}
	return len(due), nil
}

// Run rings due escalations every interval until ctx ends.
func (k *CoordinatorCalls) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	return every_(ctx, every, func() error { _, err := k.RunOnce(ctx); return err }, onErr)
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
		k.calls[e.ChannelID] = st
		k.mu.Unlock()
		if err := k.c.ARI.Answer(op, e.ChannelID); err == nil {
			k.play(op, e.ChannelID, st, k.sounds(st))
		}
	case EventDTMF:
		k.mu.Lock()
		st := k.calls[e.ChannelID]
		k.mu.Unlock()
		if st == nil || st.done {
			return
		}
		switch e.Digit {
		case "1":
			if err := k.c.Store.AcknowledgeCoordinatorCall(op, st.incident, st.seq); err != nil {
				k.c.Log.Error("coordinator acknowledgement failed", "incident", st.incident, "err", err)
				return
			}
			st.done = true
			k.play(op, e.ChannelID, st, []string{"sauti/" + k.c.Language + "/escalation-acknowledged"})
			k.hangup(op, e.ChannelID)
		case "3":
			k.play(op, e.ChannelID, st, k.sounds(st))
		}
	case EventStasisEnd, EventHangupRequest:
		k.mu.Lock()
		delete(k.calls, e.ChannelID)
		k.mu.Unlock()
	}
}

func (k *CoordinatorCalls) sounds(st *coordinatorCall) []string {
	b := "sauti/" + k.c.Language + "/"
	out := append([]string{b + "escalation-intro"}, digits(st.reference)...)
	return append(out, escalationReasonSound(k.c.Language, st.reason), b+"escalation-options")
}

func (k *CoordinatorCalls) play(ctx context.Context, ch string, st *coordinatorCall, sounds []string) {
	st.plays++
	_ = k.c.ARI.Play(ctx, ch, fmt.Sprintf("%s-c%d", ch, st.plays), sounds)
}

func (k *CoordinatorCalls) hangup(ctx context.Context, ch string) { _ = k.c.ARI.Hangup(ctx, ch) }

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
