package dispatch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/responder"
)

// Store is the dispatch worker's persistence port (the operational store,
// role op_dispatch). Every write is idempotent.
type Store interface {
	ClaimDueDispatch(ctx context.Context, limit int, lease time.Duration) ([]Due, error)
	ScheduleDispatch(ctx context.Context, incidentID uuid.UUID, gen int64, due *time.Time) error
	DispatchSnapshot(ctx context.Context, incidentID uuid.UUID) (Snapshot, error)
	Responders(ctx context.Context) ([]responder.Responder, error)
	ResponderHistory(ctx context.Context) (map[uuid.UUID]History, error)
	BusyResponders(ctx context.Context) (map[uuid.UUID]bool, error)
	Now(ctx context.Context) (time.Time, error) // the database clock (rule 3.4)

	IssueAlert(ctx context.Context, p IssueParams) (Issued, error)
	CloseAlert(ctx context.Context, alert uuid.UUID, outcome string) (bool, error)
	RecordForward(ctx context.Context, incidentID uuid.UUID, facility, gazetteerVersion, logicVersion string) (bool, error)
	TimeOutForward(ctx context.Context, incidentID uuid.UUID, facility string) (bool, error)
	NotifyCaller(ctx context.Context, incidentID uuid.UUID, kind, facility string) (bool, error)
	DispatchEscalate(ctx context.Context, incidentID uuid.UUID, reason string, role responder.Role, logicVersion string) (bool, error)
	BeginAttempt(ctx context.Context, alert uuid.UUID, purpose, channel string) (uuid.UUID, bool, error)
	FinishAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error)

	BeginNoticeAttempt(ctx context.Context, incidentID uuid.UUID, kind, channel string) (uuid.UUID, bool, error)
	BeginForwardAttempt(ctx context.Context, incidentID uuid.UUID, facility, channel string) (uuid.UUID, bool, error)
	FinishForwardAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error)
	FinishNoticeAttempt(ctx context.Context, attempt uuid.UUID, status, providerRef string) (bool, error)

	RedDispatchOutcomes(ctx context.Context, window int, redWindow time.Duration) ([]bool, error)
	AutoSuspendDispatch(ctx context.Context, failures, window int) (bool, error)
}

// Delivery is one message to a responder: an alert or a stand-down. It
// carries identifiers only; the channel adapter looks up the number.
type Delivery struct {
	AttemptID, AlertID, IncidentID, ResponderID uuid.UUID
	Role                                        responder.Role
	Purpose, Channel                            string
}

// Deliverer sends deliveries (Part B: SMS, voice). An error means the
// outcome is unknown: the attempt is settled "unknown" and never re-sent.
type Deliverer interface {
	Deliver(ctx context.Context, d Delivery) (providerRef string, err error)
}

// ErrRejected is returned by a Deliverer when the provider refused the
// message (the attempt failed; nothing was sent).
var ErrRejected = errors.New("dispatch: delivery rejected by the provider")

// Notifier delivers caller notices and forwarded facilities' SMS (the SMS
// channel); errors as Deliverer.
type Notifier interface {
	Notify(ctx context.Context, n Notice) (providerRef string, err error)
	NotifyFacility(ctx context.Context, incidentID uuid.UUID, facilityID string) (providerRef string, err error)
}

// Suspension reports whether dispatch is suspended (O6); the operational
// store implements it.
type Suspension interface {
	DispatchSuspended(ctx context.Context) (bool, error)
}

// Engine executes plans: it reads an incident's facts, plans, records each
// action through the store, delivers what the store says is undelivered,
// and stores the next wake. It holds no dispatch state of its own, so a
// crash or failover at any point resumes from the store (scenario 14).
type Engine struct {
	store      Store
	deliverer  Deliverer
	notifier   Notifier
	suspension Suspension
	params     Params
	ranker     Ranker
	logic      string
	gazetteer  location.File
	stageCells map[string]location.Cell
	rule       StoppingRule
	// passTimeout bounds each incident's pass (below the lease), so a
	// provider or lock that hangs cannot stall the leader (review I8).
	passTimeout time.Duration
	lastPass    atomic.Int64 // unix nanoseconds of the last successful RunOnce
}

// DefaultPassTimeout bounds one incident's pass; it is below the 30 s lease.
const DefaultPassTimeout = 20 * time.Second

// LastPass is when the last RunOnce completed without a store error (zero:
// never). dispatch-worker's readiness and metrics use it.
func (e *Engine) LastPass() time.Time {
	if n := e.lastPass.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// EngineConfig wires an Engine.
type EngineConfig struct {
	Store        Store
	Deliverer    Deliverer
	Notifier     Notifier
	Suspension   Suspension
	Params       Params
	Ranker       Ranker
	LogicVersion string        // stamped on every dispatch fact
	Gazetteer    location.File // stages, wards and facilities in force
	StoppingRule StoppingRule  // Guide §14 (O6)
	PassTimeout  time.Duration // default DefaultPassTimeout
}

// NewEngine validates the configuration.
func NewEngine(c EngineConfig) (*Engine, error) {
	if c.Store == nil || c.Deliverer == nil || c.Notifier == nil || c.Suspension == nil || c.LogicVersion == "" || c.Gazetteer.Version == "" {
		return nil, errors.New("dispatch: engine needs a store, deliverer, notifier, suspension source, logic version and gazetteer")
	}
	if err := c.Params.Validate(); err != nil {
		return nil, err
	}
	if c.Ranker.Version() == "" {
		return nil, errors.New("dispatch: engine needs a ranker")
	}
	if err := c.StoppingRule.Validate(); err != nil {
		return nil, err
	}
	cells := map[string]location.Cell{}
	for _, s := range c.Gazetteer.Stages {
		cells[s.ID] = s.Cell
	}
	timeout := c.PassTimeout
	if timeout <= 0 {
		timeout = DefaultPassTimeout
	}
	return &Engine{store: c.Store, deliverer: c.Deliverer, notifier: c.Notifier, suspension: c.Suspension, params: c.Params, ranker: c.Ranker,
		logic: c.LogicVersion, gazetteer: c.Gazetteer, stageCells: cells, rule: c.StoppingRule, passTimeout: timeout}, nil
}

// maxRounds bounds re-planning within one wake; each round records facts,
// so a plan that never settles is a bug, escalated rather than looped.
const maxRounds = 32

// Process plans one due incident until nothing more is due now, delivers,
// and stores the next wake for the generation it was claimed at.
func (e *Engine) Process(ctx context.Context, due Due) error {
	refused := map[uuid.UUID]bool{} // refused by the database this wake (a lapse since the read)
	for range maxRounds {
		snap, err := e.store.DispatchSnapshot(ctx, due.IncidentID)
		if err != nil {
			return err
		}
		now, err := e.store.Now(ctx)
		if err != nil {
			return err
		}
		suspended, err := e.suspension.DispatchSuspended(ctx)
		if err != nil {
			return err
		}
		st, pool, err := e.state(ctx, snap, suspended, now, refused)
		if err != nil {
			return err
		}
		acts, wake := Plan(st, pool, e.gazetteer.Facilities, e.params, now)
		if len(acts) == 0 {
			if err := e.deliver(ctx, snap); err != nil {
				return err
			}
			var next *time.Time
			if !wake.IsZero() {
				next = &wake
			}
			return e.store.ScheduleDispatch(ctx, due.IncidentID, due.Gen, next)
		}
		for _, a := range acts {
			if err := e.execute(ctx, snap.IncidentID, a, refused); err != nil {
				return fmt.Errorf("dispatch: %s for %s: %w", a.Kind, snap.IncidentID, err)
			}
		}
	}
	return fmt.Errorf("dispatch: incident %s: the plan did not settle in %d rounds", due.IncidentID, maxRounds)
}

// state builds the planner's view: the stored facts, the pool of eligible
// responders ranked, and the search centres.
func (e *Engine) state(ctx context.Context, snap Snapshot, suspended bool, now time.Time, refused map[uuid.UUID]bool) (State, Pool, error) {
	st := State{Severity: snap.Severity, Assigned: snap.Assigned, Alerts: snap.Alerts, Forwards: snap.Forwards,
		Escalations: snap.Escalations, CallerNotified: snap.CallerNotified, Suspended: suspended}
	if snap.DispatchedAt != nil {
		st.DispatchedAt = *snap.DispatchedAt
	}
	if snap.RedSince != nil {
		st.RedSince = *snap.RedSince
	}
	switch snap.State {
	case incident.StateDispatching, incident.StateAssigned, incident.StateOnScene:
	default: // closed, merged, or past the point of dispatch: stand everyone down
		st.Done = true
		return st, rankedPool{}, nil
	}
	centres, cell := e.centres(snap)
	st.Cell = cell
	rs, err := e.store.Responders(ctx)
	if err != nil {
		return st, nil, err
	}
	busy, err := e.store.BusyResponders(ctx)
	if err != nil {
		return st, nil, err
	}
	history, err := e.store.ResponderHistory(ctx)
	if err != nil {
		return st, nil, err
	}
	rs = slices.DeleteFunc(rs, func(r responder.Responder) bool { return busy[r.ID] || refused[r.ID] })
	pool := rankedPool{}
	for _, role := range responder.Roles() {
		cs := Eligible(rs, e.stageCells, role, centres, e.params.ringLimit(), now)
		for i := range cs {
			cs[i].History = history[cs[i].Responder.ID]
		}
		pool[role] = e.ranker.Rank(cs)
	}
	return st, pool, nil
}

// centres are the incident's zone, or every zone of its ward when only the
// ward is known (intake then records the ward's centre as the zone, so the
// location quality decides); cell orders facilities by distance.
func (e *Engine) centres(snap Snapshot) ([]location.Cell, location.Cell) {
	if snap.LocationQuality == incident.QualityWard {
		for _, w := range e.gazetteer.Wards {
			if w.ID == snap.Ward && len(w.Zones) > 0 {
				return w.Zones, w.Centre
			}
		}
	}
	if snap.Zone != nil {
		c := location.Cell{Col: snap.Zone.Col, Row: snap.Zone.Row}
		return []location.Cell{c}, c
	}
	for _, w := range e.gazetteer.Wards {
		if w.ID == snap.Ward {
			return w.Zones, w.Centre
		}
	}
	return nil, location.Cell{} // unknown location: nobody is eligible, so the coordinator is alerted
}

// rankedPool holds each role's eligible responders within the mode's ring
// limit, best first; Ranked filters by ring, keeping the order.
type rankedPool map[responder.Role][]Candidate

func (p rankedPool) Ranked(role responder.Role, ring int) []Candidate {
	var out []Candidate
	for _, c := range p[role] {
		if c.Distance <= ring {
			out = append(out, c)
		}
	}
	return out
}

func (e *Engine) execute(ctx context.Context, incidentID uuid.UUID, a Action, refused map[uuid.UUID]bool) error {
	var err error
	switch a.Kind {
	case ActAlert:
		_, err = e.store.IssueAlert(ctx, IssueParams{IncidentID: incidentID, ResponderID: a.ResponderID, Role: a.Role,
			Ring: a.Ring, Wave: a.Wave, LogicVersion: e.logic})
		if errors.Is(err, incident.ErrIllegalTransition) {
			// The responder lapsed or was taken since the read: leave them
			// out and re-plan; the database is the last guard (review focus 4).
			refused[a.ResponderID] = true
			return nil
		}
	case ActExpire:
		_, err = e.store.CloseAlert(ctx, a.AlertID, OutcomeNoResponse)
	case ActStandDown:
		_, err = e.store.CloseAlert(ctx, a.AlertID, OutcomeStoodDown)
	case ActEscalate:
		_, err = e.store.DispatchEscalate(ctx, incidentID, a.Reason, a.Role, e.logic)
	case ActForward:
		_, err = e.store.RecordForward(ctx, incidentID, a.FacilityID, e.gazetteer.Version, e.logic)
	case ActForwardTimeout:
		_, err = e.store.TimeOutForward(ctx, incidentID, a.FacilityID)
	case ActCallerSMS:
		_, err = e.store.NotifyCaller(ctx, incidentID, a.Message, a.FacilityID)
	default:
		err = fmt.Errorf("unknown action %q", a.Kind)
	}
	return err
}

// deliver sends every alert still pending and every stand-down that has not
// been attempted. An attempt is claimed in the store before the provider
// call and only once, so neither a retry nor a crash sends it twice; one
// whose result is unknown is never re-sent (the cascade's next rung covers
// it). Channel: SMS (USSD push is disabled, O2; voice fallback is Part B).
func (e *Engine) deliver(ctx context.Context, snap Snapshot) error {
	for _, a := range snap.Alerts {
		purpose := ""
		switch a.Outcome {
		case OutcomePending:
			purpose = PurposeAlert
		case OutcomeStoodDown:
			purpose = PurposeStandDown
		default:
			continue
		}
		attempt, fresh, err := e.store.BeginAttempt(ctx, a.ID, purpose, ChannelSMS)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		ref, derr := e.deliverer.Deliver(ctx, Delivery{AttemptID: attempt, AlertID: a.ID, IncidentID: snap.IncidentID,
			ResponderID: a.ResponderID, Role: a.Role, Purpose: purpose, Channel: ChannelSMS})
		if _, err := e.store.FinishAttempt(ctx, attempt, settlement(derr), ref); err != nil {
			return err
		}
	}
	// Caller notices the same way: each claimed once, never re-sent.
	for _, kind := range snap.CallerNotified {
		attempt, fresh, err := e.store.BeginNoticeAttempt(ctx, snap.IncidentID, kind, ChannelSMS)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		ref, nerr := e.notifier.Notify(ctx, Notice{AttemptID: attempt, IncidentID: snap.IncidentID, Kind: kind, Channel: ChannelSMS})
		if _, err := e.store.FinishNoticeAttempt(ctx, attempt, settlement(nerr), ref); err != nil {
			return err
		}
	}
	// Each forwarded facility's SMS (scenario 7), once; its call is
	// ivr-controller's.
	for _, f := range snap.Forwards {
		attempt, fresh, err := e.store.BeginForwardAttempt(ctx, snap.IncidentID, f.FacilityID, ChannelSMS)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		ref, ferr := e.notifier.NotifyFacility(ctx, snap.IncidentID, f.FacilityID)
		if _, err := e.store.FinishForwardAttempt(ctx, attempt, settlement(ferr), ref); err != nil {
			return err
		}
	}
	return nil
}

// RunOnce claims due incidents (most urgent first) and processes each. A
// failure is reported and escalated, and the lease lets it be retried.
func (e *Engine) RunOnce(ctx context.Context, batch int, lease time.Duration, onErr func(uuid.UUID, error)) (int, error) {
	due, err := e.store.ClaimDueDispatch(ctx, batch, lease)
	if err != nil {
		return 0, err
	}
	failed := false
	for _, d := range due {
		pctx, cancel := context.WithTimeout(ctx, e.passTimeout)
		err := e.Process(pctx, d)
		cancel()
		if err != nil {
			failed = true
			if ctx.Err() != nil {
				return len(due), ctx.Err()
			}
			onErr(d.IncidentID, err)
			// Fail safe to a human (rule 3.14): the coordinator sees it now.
			if _, eerr := e.store.DispatchEscalate(ctx, d.IncidentID, ReasonDispatchError, "", e.logic); eerr != nil {
				onErr(d.IncidentID, eerr)
			}
		}
	}
	if !failed {
		e.lastPass.Store(time.Now().UnixNano())
	}
	return len(due), nil
}

// ReasonDispatchError escalates an incident the worker could not process.
const ReasonDispatchError = "dispatch_error"

// ErrStoppingRuleBreached reports an automatic suspension (O6): an alarm,
// not a fault.
var ErrStoppingRuleBreached = errors.New("dispatch: stopping rule breached; dispatch suspended")

// CheckStoppingRule judges the most recent settled red incidents and, on a
// breach, suspends dispatch and registers the breach with its numbers. A
// suspended dispatch is left alone (resuming is a coordinator's decision).
func (e *Engine) CheckStoppingRule(ctx context.Context) (bool, error) {
	if on, err := e.suspension.DispatchSuspended(ctx); err != nil || on {
		return false, err
	}
	outcomes, err := e.store.RedDispatchOutcomes(ctx, e.rule.Window, e.params.RedEscalationAfter)
	if err != nil {
		return false, err
	}
	breached, failures := e.rule.Breached(outcomes)
	if !breached {
		return false, nil
	}
	return e.store.AutoSuspendDispatch(ctx, failures, e.rule.Window)
}

// Run polls for due incidents until ctx ends: at once again when a full
// batch was claimed, otherwise after poll.
func (e *Engine) Run(ctx context.Context, poll time.Duration, batch int, lease time.Duration, onErr func(uuid.UUID, error)) error {
	for {
		n, err := e.RunOnce(ctx, batch, lease, onErr)
		if err != nil && ctx.Err() == nil {
			onErr(uuid.UUID{}, err)
		}
		if breached, err := e.CheckStoppingRule(ctx); err != nil && ctx.Err() == nil {
			onErr(uuid.UUID{}, err)
		} else if breached {
			onErr(uuid.UUID{}, ErrStoppingRuleBreached)
		}
		if n >= batch && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// settlement is how an attempt settles after the provider call: sent,
// failed (the provider refused: nothing went out) or unknown (never re-sent).
func settlement(err error) string {
	switch {
	case err == nil:
		return "sent"
	case errors.Is(err, ErrRejected):
		return "failed"
	}
	return "unknown"
}
