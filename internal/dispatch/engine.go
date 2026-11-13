package dispatch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
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
	sendTimeout time.Duration // each provider call (review I4)
	workers     int           // provider calls in flight at once
	lastPass    atomic.Int64  // unix nanoseconds of the last successful RunOnce
}

// DefaultPassTimeout bounds one incident's pass; it is below the 30 s lease.
const DefaultPassTimeout = 20 * time.Second

// DefaultSendTimeout bounds each provider call, well below the pass: a
// provider that hangs costs one send timeout, not a pass (review I4).
const DefaultSendTimeout = 5 * time.Second

// DefaultDeliveryWorkers is how many provider calls run at once.
const DefaultDeliveryWorkers = 16

// settleTimeout bounds recording a send's result. It runs on a context of
// its own, so a pass that ends while a send is in flight still settles the
// attempt instead of leaving it claimed for ever.
const settleTimeout = 5 * time.Second

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
	LogicVersion string        // stamped on every fact the planner decides
	Gazetteer    location.File // stages, wards and facilities in force
	StoppingRule StoppingRule  // Guide §14 (O6)
	PassTimeout  time.Duration // default DefaultPassTimeout
	SendTimeout  time.Duration // default DefaultSendTimeout
	Workers      int           // provider calls at once; default DefaultDeliveryWorkers
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
		logic: c.LogicVersion, gazetteer: c.Gazetteer, stageCells: cells, rule: c.StoppingRule, passTimeout: timeout,
		sendTimeout: c.SendTimeout, workers: c.Workers}, nil
}

// maxRounds bounds re-planning within one wake; each round records facts,
// so a plan that never settles is a bug, escalated rather than looped.
const maxRounds = 32

// Process plans one due incident until nothing more is due now, delivers,
// and stores the next wake for the generation it was claimed at.
func (e *Engine) Process(ctx context.Context, due Due) error {
	p, err := e.plan(ctx, due)
	if err != nil {
		return err
	}
	if err := e.deliverAll(ctx, []Snapshot{p.snap})[due.IncidentID]; err != nil {
		return err
	}
	return e.store.ScheduleDispatch(ctx, due.IncidentID, due.Gen, p.next)
}

// planned is an incident whose facts due now are recorded: its sends are
// still to make and its next wake still to store. The wake is stored only
// once the sends have been made, so a pass that fails while sending leaves
// the incident leased and the next pass after the lease retries them.
type planned struct {
	due  Due
	snap Snapshot
	next *time.Time
}

// plan records every fact due now for one incident.
func (e *Engine) plan(ctx context.Context, due Due) (planned, error) {
	// Refused by the database this wake: a responder who lapsed since the
	// read, a facility it would not take (a suspension committed since the
	// read). The next round re-reads the facts without them.
	refused := refusals{responders: map[uuid.UUID]bool{}, facilities: map[string]bool{}}
	for range maxRounds {
		snap, err := e.store.DispatchSnapshot(ctx, due.IncidentID)
		if err != nil {
			return planned{}, err
		}
		now, err := e.store.Now(ctx)
		if err != nil {
			return planned{}, err
		}
		suspended, err := e.suspension.DispatchSuspended(ctx)
		if err != nil {
			return planned{}, err
		}
		st, pool, err := e.state(ctx, snap, suspended, now, refused.responders)
		if err != nil {
			return planned{}, err
		}
		facilities := slices.DeleteFunc(slices.Clone(e.gazetteer.Facilities), func(f location.Facility) bool { return refused.facilities[f.ID] })
		acts, wake := Plan(st, pool, facilities, e.params, now)
		if len(acts) == 0 {
			p := planned{due: due, snap: snap}
			if !wake.IsZero() {
				p.next = &wake
			}
			return p, nil
		}
		for _, a := range acts {
			if err := e.execute(ctx, snap.IncidentID, a, refused); err != nil {
				return planned{}, fmt.Errorf("dispatch: %s for %s: %w", a.Kind, snap.IncidentID, err)
			}
		}
	}
	return planned{}, fmt.Errorf("dispatch: incident %s: the plan did not settle in %d rounds", due.IncidentID, maxRounds)
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

// refusals are what the database refused during one wake.
type refusals struct {
	responders map[uuid.UUID]bool
	facilities map[string]bool
}

func (e *Engine) execute(ctx context.Context, incidentID uuid.UUID, a Action, refused refusals) error {
	var err error
	switch a.Kind {
	case ActAlert:
		_, err = e.store.IssueAlert(ctx, IssueParams{IncidentID: incidentID, ResponderID: a.ResponderID, Role: a.Role,
			Ring: a.Ring, Wave: a.Wave, LogicVersion: e.logic})
		if errors.Is(err, incident.ErrIllegalTransition) {
			// The responder lapsed or was taken since the read: leave them
			// out and re-plan; the database is the last guard (review focus 4).
			refused.responders[a.ResponderID] = true
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
		if errors.Is(err, incident.ErrIllegalTransition) {
			// Suspended since the read (or a facility the database will not
			// take): re-plan without it; the next round reads the suspension.
			refused.facilities[a.FacilityID] = true
			return nil
		}
	case ActForwardTimeout:
		_, err = e.store.TimeOutForward(ctx, incidentID, a.FacilityID)
	case ActCallerSMS:
		_, err = e.store.NotifyCaller(ctx, incidentID, a.Message, a.FacilityID)
	default:
		err = fmt.Errorf("unknown action %q", a.Kind)
	}
	return err
}

// sends lists one planned incident's sends: every alert still pending and
// every stand-down not yet attempted, the caller notices and the forwarded
// facilities' SMS. Each is claimed in the store before the provider call and
// only once, so neither a retry nor a crash sends it twice; one whose result
// is unknown is never re-sent (the next rung covers it). Each provider call
// is bounded by the send timeout and its result settled on a context of its
// own (review I4).
func (e *Engine) sends(snap Snapshot) []func(context.Context) error {
	var out []func(context.Context) error
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
		out = append(out, func(ctx context.Context) error {
			attempt, fresh, err := e.store.BeginAttempt(ctx, a.ID, purpose, ChannelSMS)
			if err != nil || !fresh {
				return err
			}
			ref, derr := e.send(ctx, func(c context.Context) (string, error) {
				return e.deliverer.Deliver(c, Delivery{AttemptID: attempt, AlertID: a.ID, IncidentID: snap.IncidentID,
					ResponderID: a.ResponderID, Role: a.Role, Purpose: purpose, Channel: ChannelSMS})
			})
			return e.settle(ctx, func(c context.Context) error {
				_, err := e.store.FinishAttempt(c, attempt, settlement(derr), ref)
				return err
			})
		})
	}
	for _, kind := range snap.CallerNotified {
		out = append(out, func(ctx context.Context) error {
			attempt, fresh, err := e.store.BeginNoticeAttempt(ctx, snap.IncidentID, kind, ChannelSMS)
			if err != nil || !fresh {
				return err
			}
			ref, nerr := e.send(ctx, func(c context.Context) (string, error) {
				return e.notifier.Notify(c, Notice{AttemptID: attempt, IncidentID: snap.IncidentID, Kind: kind, Channel: ChannelSMS})
			})
			return e.settle(ctx, func(c context.Context) error {
				_, err := e.store.FinishNoticeAttempt(c, attempt, settlement(nerr), ref)
				return err
			})
		})
	}
	// Each forwarded facility's SMS (scenario 7); its call is ivr-controller's.
	for _, f := range snap.Forwards {
		out = append(out, func(ctx context.Context) error {
			attempt, fresh, err := e.store.BeginForwardAttempt(ctx, snap.IncidentID, f.FacilityID, ChannelSMS)
			if err != nil || !fresh {
				return err
			}
			ref, ferr := e.send(ctx, func(c context.Context) (string, error) {
				return e.notifier.NotifyFacility(c, snap.IncidentID, f.FacilityID)
			})
			return e.settle(ctx, func(c context.Context) error {
				_, err := e.store.FinishForwardAttempt(c, attempt, settlement(ferr), ref)
				return err
			})
		})
	}
	return out
}

// pipeline makes planned incidents' sends on at most workers provider calls
// at once, shared by a whole batch, in the order the incidents are added
// (the most urgent first). An incident's sends start as soon as it is added,
// while later incidents are still planning (CodeRabbit PR #11): neither a
// slow provider nor a slow or failing plan delays an alert already issued.
type pipeline struct {
	ctx       context.Context
	queue     chan func()
	turn      chan struct{} // closed once the last added incident's sends are all queued
	workers   sync.WaitGroup
	incidents sync.WaitGroup
}

func (e *Engine) newPipeline(ctx context.Context) *pipeline {
	n := e.workers
	if n <= 0 {
		n = DefaultDeliveryWorkers
	}
	p := &pipeline{ctx: ctx, queue: make(chan func()), turn: make(chan struct{})}
	close(p.turn)
	for range n {
		p.workers.Go(func() {
			for f := range p.queue {
				f()
			}
		})
	}
	return p
}

// add queues an incident's sends behind those of the incidents added before
// it and, once they have all been made, calls done with the first error (nil
// when every send was made and settled). add never waits for a send.
func (p *pipeline) add(sends []func(context.Context) error, done func(error)) {
	prev, mine := p.turn, make(chan struct{})
	p.turn = mine
	p.incidents.Go(func() {
		var made sync.WaitGroup
		var mu sync.Mutex
		var first error
		<-prev
		for _, send := range sends {
			made.Add(1)
			p.queue <- func() {
				defer made.Done()
				if err := send(p.ctx); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		}
		close(mine)
		made.Wait()
		done(first)
	})
}

// wait returns once every added incident's sends are made and its done has
// returned; the workers then stop.
func (p *pipeline) wait() {
	p.incidents.Wait()
	close(p.queue)
	p.workers.Wait()
}

// deliverAll makes the sends of the given incidents (at most workers at
// once, the first incident first) and returns each incident's first store
// error.
func (e *Engine) deliverAll(ctx context.Context, snaps []Snapshot) map[uuid.UUID]error {
	errs := map[uuid.UUID]error{}
	var mu sync.Mutex
	p := e.newPipeline(ctx)
	for _, snap := range snaps {
		p.add(e.sends(snap), func(err error) {
			if err != nil {
				mu.Lock()
				errs[snap.IncidentID] = err
				mu.Unlock()
			}
		})
	}
	p.wait()
	return errs
}

// send makes one provider call under the send timeout.
func (e *Engine) send(ctx context.Context, call func(context.Context) (string, error)) (string, error) {
	t := e.sendTimeout
	if t <= 0 {
		t = DefaultSendTimeout
	}
	c, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	return call(c)
}

// settle records a send's result on a context of its own, so the end of the
// pass does not leave the attempt claimed for ever.
func (e *Engine) settle(ctx context.Context, f func(context.Context) error) error {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	return f(c)
}

// RunOnce claims due incidents (most urgent first) and processes each. A
// failure is reported and escalated, and the lease lets it be retried.
func (e *Engine) RunOnce(ctx context.Context, batch int, lease time.Duration, onErr func(uuid.UUID, error)) (int, error) {
	due, err := e.store.ClaimDueDispatch(ctx, batch, lease)
	if err != nil {
		return 0, err
	}
	// Sends and their wakes finish on the pipeline's goroutines while the
	// next incident plans: failures are reported one at a time.
	var mu sync.Mutex
	failed, interrupted := false, false
	fail := func(id uuid.UUID, err error) {
		mu.Lock()
		failed = true
		onErr(id, err)
		mu.Unlock()
		// Fail safe to a human (rule 3.14): the coordinator sees it now.
		if _, eerr := e.store.DispatchEscalate(ctx, id, ReasonDispatchError, "", e.logic); eerr != nil {
			mu.Lock()
			onErr(id, eerr)
			mu.Unlock()
		}
	}
	if len(due) > 0 {
		// Each incident is planned in turn (its alerts issued, T3 recorded),
		// most urgent first, and its sends start at once on the batch's
		// shared workers while the next one plans (review I4; CodeRabbit PR
		// #11). Its next wake is stored only after its sends: a failure
		// while sending leaves it leased, and the pass after the lease
		// retries it.
		p := e.newPipeline(ctx)
		for _, d := range due {
			pctx, cancel := context.WithTimeout(ctx, e.passTimeout)
			pl, err := e.plan(pctx, d)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					interrupted = true
					break
				}
				fail(d.IncidentID, err)
				continue
			}
			p.add(e.sends(pl.snap), func(err error) {
				if err == nil {
					err = e.store.ScheduleDispatch(ctx, pl.due.IncidentID, pl.due.Gen, pl.next)
				}
				if err == nil {
					return
				}
				if ctx.Err() != nil {
					mu.Lock()
					interrupted = true
					mu.Unlock()
					return
				}
				fail(pl.due.IncidentID, err)
			})
		}
		p.wait()
	}
	if interrupted {
		return len(due), ctx.Err()
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
