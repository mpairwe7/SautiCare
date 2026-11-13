package dispatch

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/responder"
)

// Modes of the dispatch layer (Build Prompt §10.4, §19 decision 2). Protocol
// mode keeps the search at ring 1 and escalates to the coordinator instead of
// widening or forwarding, until the amendment is approved.
const (
	ModeProtocol = "protocol"
	ModeAmended  = "amended"
)

// Params are the versioned dispatch parameters (config/gated/dispatch.yaml).
type Params struct {
	Mode               string
	RingInterval       time.Duration // 60 s: one ring step
	MaxRing            int           // 5: 2.5 km in 500 m steps
	CascadeTimeout     time.Duration // 90 s: an unanswered alert lapses
	RedParallel        int           // 3 per role per ring step
	ForwardAckTimeout  time.Duration // 60 s for a facility to press 1
	RedEscalationAfter time.Duration // 3 min: red with a role unaccepted (decision 2)
	// FollowUpAfter: an incident still open this long after its first
	// acceptance is escalated once so a coordinator checks with the caller
	// (interim until M3's arrival verification; ADR-0028).
	FollowUpAfter time.Duration
}

// Validate refuses parameters the planner cannot run safely.
func (p Params) Validate() error {
	switch {
	case p.Mode != ModeProtocol && p.Mode != ModeAmended:
		return fmt.Errorf("dispatch: mode %q is neither %s nor %s", p.Mode, ModeProtocol, ModeAmended)
	case p.RingInterval <= 0, p.CascadeTimeout <= 0, p.ForwardAckTimeout <= 0, p.RedEscalationAfter <= 0, p.FollowUpAfter <= 0:
		return errors.New("dispatch: every interval must be positive")
	case p.MaxRing < 1, p.RedParallel < 1:
		return errors.New("dispatch: maximum ring and red parallel count must be at least 1")
	}
	return nil
}

// ringLimit is the widest ring the mode allows.
func (p Params) ringLimit() int {
	if p.Mode == ModeAmended {
		return p.MaxRing
	}
	return 1
}

// Alert outcomes.
const (
	OutcomePending    = "pending"
	OutcomeAccepted   = "accepted"
	OutcomeDeclined   = "declined"
	OutcomeNoResponse = "no_response"
	OutcomeStoodDown  = "stood_down"
)

// Alert is one responder alerted for the incident.
type Alert struct {
	ID          uuid.UUID
	ResponderID uuid.UUID
	Role        responder.Role
	Ring        int // the ring in force when the alert was sent
	Wave        int // red: ring step (1 = at T2); yellow and green: cascade depth
	SentAt      time.Time
	Outcome     string
}

// ForwardState is one facility the request was forwarded to.
type ForwardState struct {
	FacilityID string
	SentAt     time.Time
	Acked      bool
	TimedOut   bool
}

// Escalation reasons (the coordinator's alert says why).
const (
	ReasonSuspended           = "dispatch_suspended"
	ReasonExhausted           = "no_eligible_responder"
	ReasonNoneNearby          = "no_responder_nearby" // amended mode: ring 1 empty at T2 (the search widens)
	ReasonFollowUp            = "assigned_follow_up"  // still open FollowUpAfter after acceptance: a coordinator checks with the caller
	ReasonRedNoAcceptance     = "red_no_acceptance"
	ReasonForwarded           = "forwarded_to_facility"
	ReasonForwardingExhausted = "no_facility_acknowledged"
)

// Caller message kinds (SMS templates, Part B).
const (
	CallerHelperOnTheWay = incident.NoticeHelperOnTheWay
	CallerForwarded      = incident.NoticeForwarded
)

// State is everything the planner needs about one incident, rebuilt from the
// operational store before each run.
type State struct {
	Severity       incident.Severity
	DispatchedAt   time.Time // T2
	Cell           location.Cell
	Assigned       map[responder.Role]uuid.UUID // first acceptance per role
	Alerts         []Alert
	Forwards       []ForwardState
	Escalations    []string  // escalation keys already recorded (incident.EscalationKey)
	RedSince       time.Time // when the incident became red (zero: from T2)
	AcceptedAt     time.Time // the first open acceptance (zero: none)
	CallerNotified []string  // caller message kinds already sent
	Suspended      bool      // dispatch suspended (O6)
	Done           bool      // closed or merged
}

// Pool returns the eligible responders of a role within ring k of the
// incident, ranked best first (Eligible + Ranker, minus busy responders).
type Pool interface {
	Ranked(role responder.Role, ring int) []Candidate
}

// ActionKind names what the worker must do.
type ActionKind string

const (
	ActAlert          ActionKind = "alert"
	ActExpire         ActionKind = "expire"
	ActStandDown      ActionKind = "stand_down"
	ActEscalate       ActionKind = "escalate"
	ActForward        ActionKind = "forward"
	ActForwardTimeout ActionKind = "forward_timeout"
	ActCallerSMS      ActionKind = "caller_sms"
)

// Action is one step for the worker. Executing it is idempotent: the
// worker keys each on the incident and these fields.
type Action struct {
	Kind        ActionKind
	AlertID     uuid.UUID // expire and stand-down
	ResponderID uuid.UUID
	Role        responder.Role
	Ring, Wave  int
	Reason      string
	FacilityID  string
	Message     string
}

// Plan decides what to do for one incident at now, and when to look again
// (zero: nothing scheduled; the worker re-plans on the next recorded fact).
// It is pure: the same state, pool and time give the same actions. The
// worker applies the actions, records them, and calls Plan again at once
// until it returns none, then sleeps until the wake time.
func Plan(s State, pool Pool, facilities []location.Facility, p Params, now time.Time) ([]Action, time.Time) {
	var acts []Action
	var wake time.Time
	soon := func(t time.Time) {
		if t.After(now) && (wake.IsZero() || t.Before(wake)) {
			wake = t
		}
	}

	// A triage error makes the incident red (rule 3.3): anything that is not
	// yellow or green is planned as red.
	red := s.Severity != incident.SevYellow && s.Severity != incident.SevGreen

	// Closed or merged: stand down everyone still on the way to it.
	if s.Done {
		for _, a := range s.Alerts {
			if a.Outcome == OutcomePending {
				acts = append(acts, Action{Kind: ActStandDown, AlertID: a.ID, ResponderID: a.ResponderID, Role: a.Role})
			}
		}
		return acts, time.Time{}
	}
	if s.DispatchedAt.IsZero() {
		return nil, time.Time{} // not dispatched: nothing to plan
	}

	// Facts first: lapsed alerts and stand-downs after an acceptance. These
	// hold while suspended too — an alert already sent may still be taken.
	for _, a := range s.Alerts {
		if a.Outcome != OutcomePending {
			continue
		}
		if _, taken := s.Assigned[a.Role]; taken {
			acts = append(acts, Action{Kind: ActStandDown, AlertID: a.ID, ResponderID: a.ResponderID, Role: a.Role})
			continue
		}
		due := a.SentAt.Add(p.CascadeTimeout)
		if !now.Before(due) {
			acts = append(acts, Action{Kind: ActExpire, AlertID: a.ID, ResponderID: a.ResponderID, Role: a.Role})
		} else {
			soon(due)
		}
	}
	if len(acts) > 0 {
		return acts, time.Time{} // apply these, then re-plan at once
	}
	if len(s.Assigned) > 0 && !slices.Contains(s.CallerNotified, CallerHelperOnTheWay) {
		return []Action{{Kind: ActCallerSMS, Message: CallerHelperOnTheWay}}, time.Time{}
	}

	// Once per reason and role: a second role's exhaustion is its own alert.
	escalate := func(reason string, role responder.Role) {
		key := incident.EscalationKey(reason, string(role))
		if !slices.Contains(s.Escalations, key) && !slices.ContainsFunc(acts, func(a Action) bool {
			return a.Kind == ActEscalate && incident.EscalationKey(a.Reason, string(a.Role)) == key
		}) {
			acts = append(acts, Action{Kind: ActEscalate, Reason: reason, Role: role})
		}
	}

	// Interim until M3's arrival verification (ADR-0028, rule 14): an
	// incident still open FollowUpAfter after its first acceptance is
	// escalated once, so a coordinator checks with the caller that help came.
	if !s.AcceptedAt.IsZero() {
		if at := s.AcceptedAt.Add(p.FollowUpAfter); !now.Before(at) {
			escalate(ReasonFollowUp, "")
		} else {
			soon(at)
		}
	}

	// Suspended (O6): no responder is alerted, nothing new is forwarded; the
	// coordinator has the incident. Lapses are still recorded, and forwards
	// already made are kept up (a timeout, the caller told of an
	// acknowledgement).
	if s.Suspended {
		escalate(ReasonSuspended, "")
		settled, _ := settleForwards(s, p, now, soon)
		return append(acts, settled...), wake
	}

	elapsed := now.Sub(s.DispatchedAt)
	step := int(elapsed / p.RingInterval) // 0 at T2
	ring := min(1+step, p.ringLimit())
	alerted := map[uuid.UUID]bool{}
	for _, a := range s.Alerts {
		alerted[a.ResponderID] = true
	}
	open := []responder.Role{} // roles with no acceptance yet
	for _, role := range responder.Roles() {
		if _, taken := s.Assigned[role]; !taken {
			open = append(open, role)
		}
	}

	for _, role := range open {
		var pending, wave int
		for _, a := range s.Alerts {
			if a.Role != role {
				continue
			}
			if a.Outcome == OutcomePending {
				pending++
			}
			if !red {
				wave++ // cascade depth so far
			} else if a.Wave == step+1 && !a.SentAt.Before(s.RedSince) {
				wave++ // red: sent in this ring step since the incident became red
			}
		}
		fresh := func(k int) []Candidate {
			var out []Candidate
			for _, c := range pool.Ranked(role, k) {
				if !alerted[c.Responder.ID] {
					out = append(out, c)
				}
			}
			return out
		}
		var send []Candidate
		w := 0
		if red {
			// Top RedParallel at T2, and at each ring step the top RedParallel
			// not yet alerted in the (possibly wider) ring.
			c := fresh(ring)
			send = c[:min(len(c), max(0, p.RedParallel-wave))]
			w = step + 1
		} else if pending == 0 {
			// One at a time: the next pick comes from the current ring.
			c := fresh(ring)
			send = c[:min(len(c), 1)]
			w = wave + 1
		}
		for _, c := range send {
			acts = append(acts, Action{Kind: ActAlert, ResponderID: c.Responder.ID, Role: role, Ring: ring, Wave: w})
			alerted[c.Responder.ID] = true
			pending++
		}
		// Nobody left to try inside the mode's limit and nobody on the way:
		// the coordinator is alerted now (dispatch never ends silently).
		if pending == 0 && len(fresh(p.ringLimit())) == 0 {
			escalate(ReasonExhausted, role)
		}
		// Amended mode: nobody at all in ring 1 at T2 — the coordinator hears
		// it now, while the search widens (review focus 3).
		if p.Mode == ModeAmended && step == 0 && len(pool.Ranked(role, 1)) == 0 && !slices.ContainsFunc(s.Alerts, func(a Alert) bool { return a.Role == role }) {
			escalate(ReasonNoneNearby, role)
		}
	}
	if len(open) == 0 {
		// Both roles taken: no new forward, but one already made still times
		// out or, acknowledged, is still told to the caller.
		settled, _ := settleForwards(s, p, now, soon)
		return append(acts, settled...), wake
	}
	// The next ring step: wider ring, a new red wave, or someone switched on.
	soon(s.DispatchedAt.Add(time.Duration(step+1) * p.RingInterval))

	// Decision 2: red with a role still unaccepted at 3 minutes.
	if red {
		at := s.DispatchedAt.Add(p.RedEscalationAfter)
		if !now.Before(at) {
			for _, role := range open {
				escalate(ReasonRedNoAcceptance, role)
			}
		} else {
			soon(at)
		}
	}

	// Amended mode: at 2.5 km with no acceptance, forward to the nearest
	// facility with forwarding enabled; the next nearest after 60 s without
	// acknowledgement. The search goes on meanwhile.
	if p.Mode == ModeAmended {
		at := s.DispatchedAt.Add(time.Duration(p.MaxRing) * p.RingInterval)
		if now.Before(at) {
			soon(at)
			return acts, wake
		}
		escalate(ReasonForwarded, open[0])
		acts = append(acts, forward(s, facilities, p, now, soon)...)
	}
	return acts, wake
}

// settleForwards keeps up the forwards already made, and never starts one:
// the caller is told which centre acknowledged, and an unacknowledged
// forward times out after ForwardAckTimeout. held reports that the ladder
// must not move on (a centre acknowledged, or the last forward awaits its
// acknowledgement). It runs on every exit that stops the search, so a
// forward is never left open (CodeRabbit PR #10).
func settleForwards(s State, p Params, now time.Time, soon func(time.Time)) (acts []Action, held bool) {
	for _, f := range s.Forwards {
		if f.Acked {
			if !slices.Contains(s.CallerNotified, CallerForwarded) {
				return []Action{{Kind: ActCallerSMS, Message: CallerForwarded, FacilityID: f.FacilityID}}, true
			}
			return nil, true
		}
	}
	if n := len(s.Forwards); n > 0 && !s.Forwards[n-1].TimedOut {
		last := s.Forwards[n-1]
		due := last.SentAt.Add(p.ForwardAckTimeout)
		if now.Before(due) {
			soon(due)
			return nil, true
		}
		return []Action{{Kind: ActForwardTimeout, FacilityID: last.FacilityID}}, true
	}
	return nil, false
}

// forward plans the facility ladder once forwarding has started.
func forward(s State, facilities []location.Facility, p Params, now time.Time, soon func(time.Time)) []Action {
	if acts, held := settleForwards(s, p, now, soon); held {
		return acts
	}
	tried := map[string]bool{}
	for _, f := range s.Forwards {
		tried[f.FacilityID] = true
	}
	for _, f := range nearest(facilities, s.Cell) {
		if !tried[f.ID] {
			return []Action{{Kind: ActForward, FacilityID: f.ID}}
		}
	}
	if !HasEscalation(s.Escalations, ReasonForwardingExhausted) {
		return []Action{{Kind: ActEscalate, Reason: ReasonForwardingExhausted}}
	}
	return nil
}

// nearest returns the forwarding-enabled facilities by zone distance from
// the incident, gazetteer order breaking ties.
func nearest(fs []location.Facility, from location.Cell) []location.Facility {
	var out []location.Facility
	for _, f := range fs {
		if f.ForwardingEnabled {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b location.Facility) int {
		return cmp.Compare(from.Chebyshev(a.Cell), from.Chebyshev(b.Cell))
	})
	return out
}

// HasEscalation reports whether keys holds an escalation for reason (any role).
func HasEscalation(keys []string, reason string) bool {
	prefix := incident.EscalationKey(reason, "")
	prefix = prefix[:len(prefix)-1] // "escalate:<reason>:"
	return slices.ContainsFunc(keys, func(k string) bool { return len(k) > len(prefix) && k[:len(prefix)] == prefix })
}
