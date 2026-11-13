package dispatch

import (
	"fmt"
	"slices"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/responder"
)

var amended = Params{Mode: ModeAmended, RingInterval: 60 * time.Second, MaxRing: 5, CascadeTimeout: 90 * time.Second,
	RedParallel: 3, ForwardAckTimeout: 60 * time.Second, RedEscalationAfter: 3 * time.Minute, FollowUpAfter: 20 * time.Minute}

var protocol = func() Params { p := amended; p.Mode = ModeProtocol; return p }()

// pool places n responders of each role at each Chebyshev distance listed.
type pool map[responder.Role][]Candidate

func (p pool) Ranked(role responder.Role, ring int) []Candidate {
	var out []Candidate
	for _, c := range p[role] {
		if c.Distance <= ring {
			out = append(out, c)
		}
	}
	return out
}

func makePool(perDistance map[int]int) pool {
	p := pool{}
	for _, role := range responder.Roles() {
		for d := 0; d <= 5; d++ {
			for range perDistance[d] {
				p[role] = append(p[role], Candidate{Responder: responder.Responder{ID: uuid.NewV7(), Role: role}, Distance: d})
			}
		}
	}
	return p
}

// Facilities in gazetteer order, not distance order: the planner forwards
// to the nearest forwarding-enabled one first, and never to the others.
var facilities = []location.Facility{
	{ID: "hc_next", ForwardingEnabled: true, Cell: location.Cell{Col: 905, Row: 84}},
	{ID: "hc_closest_no_forwarding", Cell: centre},
	{ID: "hc_near", ForwardingEnabled: true, Cell: location.Cell{Col: 903, Row: 83}},
}

// sim drives Plan the way dispatch-worker does: apply the actions to the
// state, then wake at the next wake time. Each alert gets an ID, as the store
// gives it, and an expiry or stand-down closes the alert its AlertID names:
// a planner naming the wrong alert fails the test (CodeRabbit PR #10).
type sim struct {
	t     *testing.T
	s     State
	p     pool
	par   Params
	now   time.Time
	log   []Action
	react func(*sim, Action) // responder/facility behaviour
}

func newSim(t *testing.T, sev incident.Severity, p pool, par Params) *sim {
	return &sim{t: t, s: State{Severity: sev, DispatchedAt: t0, Cell: centre, Assigned: map[responder.Role]uuid.UUID{}}, p: p, par: par, now: t0}
}

func (m *sim) apply(a Action) {
	switch a.Kind {
	case ActAlert:
		m.s.Alerts = append(m.s.Alerts, Alert{ID: uuid.NewV7(), ResponderID: a.ResponderID, Role: a.Role, Ring: a.Ring, Wave: a.Wave,
			SentAt: m.now, Outcome: OutcomePending})
	case ActExpire, ActStandDown:
		i := slices.IndexFunc(m.s.Alerts, func(x Alert) bool { return x.ID == a.AlertID })
		if i < 0 || m.s.Alerts[i].ResponderID != a.ResponderID || m.s.Alerts[i].Outcome != OutcomePending {
			m.t.Fatalf("%s names alert %v, which is not the pending alert of responder %v", a.Kind, a.AlertID, a.ResponderID)
		}
		m.s.Alerts[i].Outcome = map[ActionKind]string{ActExpire: OutcomeNoResponse, ActStandDown: OutcomeStoodDown}[a.Kind]
	case ActEscalate:
		m.s.Escalations = append(m.s.Escalations, incident.EscalationKey(a.Reason, string(a.Role)))
	case ActForward:
		m.s.Forwards = append(m.s.Forwards, ForwardState{FacilityID: a.FacilityID, SentAt: m.now})
	case ActForwardTimeout:
		for i := range m.s.Forwards {
			if m.s.Forwards[i].FacilityID == a.FacilityID {
				m.s.Forwards[i].TimedOut = true
			}
		}
	case ActCallerSMS:
		m.s.CallerNotified = append(m.s.CallerNotified, a.Message)
	}
	m.log = append(m.log, a)
	if m.react != nil {
		m.react(m, a)
	}
}

func (m *sim) ack(facility string) {
	for i := range m.s.Forwards {
		if m.s.Forwards[i].FacilityID == facility {
			m.s.Forwards[i].Acked = true
		}
	}
}

func (m *sim) accept(id uuid.UUID) {
	for i := range m.s.Alerts {
		if m.s.Alerts[i].ResponderID == id {
			m.s.Alerts[i].Outcome = OutcomeAccepted
			m.s.Assigned[m.s.Alerts[i].Role] = id
		}
	}
}

// run plans until time limit, returning when nothing more is due.
func (m *sim) run(until time.Time) {
	for range 200 {
		acts, wake := Plan(m.s, m.p, facilities, m.par, m.now)
		for _, a := range acts {
			m.apply(a)
		}
		if len(acts) > 0 {
			continue // re-plan at the same instant after applying
		}
		if wake.IsZero() || wake.After(until) {
			return
		}
		m.now = wake
	}
	m.t.Fatal("planner did not settle")
}

func (m *sim) alerts(role responder.Role) []Alert {
	var out []Alert
	for _, a := range m.s.Alerts {
		if a.Role == role {
			out = append(out, a)
		}
	}
	return out
}

func (m *sim) count(kind ActionKind) int {
	n := 0
	for _, a := range m.log {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

// Red: the top 3 of each role in parallel at once (Build Prompt §10.4).
func TestRedAlertsTopThreePerRoleInParallel(t *testing.T) {
	m := newSim(t, incident.SevRed, makePool(map[int]int{0: 2, 1: 3}), amended)
	m.run(t0)
	for _, role := range responder.Roles() {
		as := m.alerts(role)
		if len(as) != 3 || !as[0].SentAt.Equal(t0) {
			t.Fatalf("%s: %d alerts at T2, want 3", role, len(as))
		}
	}
}

// Red, at each ring step, also alerts the top 3 of the role not yet alerted
// in the (wider) ring: a closer responder left over from an inner ring is
// never skipped for a farther one.
func TestRedAlertsTopThreeNotYetAlertedAtEachRingStep(t *testing.T) {
	p := makePool(map[int]int{1: 3, 2: 4, 3: 1})
	m := newSim(t, incident.SevRed, p, amended)
	m.run(t0.Add(2*time.Minute + time.Second))
	as := m.alerts(responder.RoleVHT)
	byRing := map[int]int{}
	for _, a := range as {
		byRing[a.Ring]++
	}
	if byRing[1] != 3 || byRing[2] != 3 || byRing[3] != 2 || len(as) != 8 {
		t.Fatalf("waves by ring = %v (%d alerts)", byRing, len(as))
	}
	leftover := p[responder.RoleVHT][6] // the 4th responder at distance 2
	if !slices.ContainsFunc(as, func(a Alert) bool { return a.ResponderID == leftover.Responder.ID && a.Ring == 3 }) {
		t.Fatal("the closer leftover responder was skipped at ring 3")
	}
}

// Scenario 2: yellow obstetric — the VHT cascade reaches depth 3 (one at a
// time, 90 s each); the transporter accepts at once.
func TestScenario2YellowCascadeDepthThree(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{0: 1, 1: 4}), amended)
	m.react = func(m *sim, a Action) {
		if a.Kind == ActAlert && a.Role == responder.RoleTransporter {
			m.accept(a.ResponderID)
		}
	}
	vhts := map[int]uuid.UUID{}
	for depth := 1; depth <= 3; depth++ {
		m.run(t0.Add(time.Duration(depth-1) * 90 * time.Second))
		as := m.alerts(responder.RoleVHT)
		if len(as) != depth {
			t.Fatalf("depth %d: %d VHT alerts", depth, len(as))
		}
		vhts[depth] = as[depth-1].ResponderID
		if depth > 1 && !as[depth-1].SentAt.Equal(t0.Add(time.Duration(depth-1)*90*time.Second)) {
			t.Fatalf("depth %d sent at %v", depth, as[depth-1].SentAt)
		}
	}
	m.accept(vhts[3])
	m.run(t0.Add(10 * time.Minute))
	if len(m.alerts(responder.RoleTransporter)) != 1 || len(m.alerts(responder.RoleVHT)) != 3 || m.count(ActEscalate) != 0 {
		t.Fatalf("transporters %d, VHTs %d, escalations %d", len(m.alerts(responder.RoleTransporter)), len(m.alerts(responder.RoleVHT)), m.count(ActEscalate))
	}
}

func (m *sim) forwarded() []string {
	var out []string
	for _, a := range m.log {
		if a.Kind == ActForward {
			out = append(out, a.FacilityID)
		}
	}
	return out
}

// Scenario 7: nobody accepts — rings widen every 60 s to 2.5 km (ring 5),
// then the nearest forwarding facility is called and the coordinator alerted
// at the same moment; the caller's SMS names the centre that acknowledged.
func TestScenario7NobodyAcceptsForwardsToNearest(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{}), amended)
	m.react = func(m *sim, a Action) {
		if a.Kind == ActForward {
			m.ack(a.FacilityID)
		}
	}
	m.run(t0.Add(4*time.Minute + 59*time.Second))
	if len(m.forwarded()) != 0 {
		t.Fatal("forwarded before ring 5 had its interval")
	}
	m.run(t0.Add(15 * time.Minute))
	if got := m.forwarded(); !slices.Equal(got, []string{"hc_near"}) || !m.s.Forwards[0].SentAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("forwards = %v at %v", got, m.s.Forwards)
	}
	if !HasEscalation(m.s.Escalations, ReasonForwarded) || !slices.Contains(m.s.CallerNotified, CallerForwarded) {
		t.Fatalf("escalations %v caller %v", m.s.Escalations, m.s.CallerNotified)
	}
	for _, a := range m.log {
		if a.Kind == ActCallerSMS && a.Message == CallerForwarded && a.FacilityID != "hc_near" {
			t.Fatalf("caller told about %s", a.FacilityID)
		}
	}
}

// Scenario 8: the first centre does not acknowledge within 60 s — the next
// nearest is called; the caller hears about the one that acknowledged.
func TestScenario8NoAcknowledgementTriesNextNearest(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{}), amended)
	m.react = func(m *sim, a Action) {
		if a.Kind == ActForward && a.FacilityID == "hc_next" {
			m.ack(a.FacilityID)
		}
	}
	m.run(t0.Add(15 * time.Minute))
	if got := m.forwarded(); !slices.Equal(got, []string{"hc_near", "hc_next"}) ||
		!m.s.Forwards[1].SentAt.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("forwards = %v %v", got, m.s.Forwards)
	}
	if m.count(ActCallerSMS) != 1 || HasEscalation(m.s.Escalations, ReasonForwardingExhausted) {
		t.Fatalf("caller SMS %d, escalations %v", m.count(ActCallerSMS), m.s.Escalations)
	}
}

// No centre acknowledges: the coordinator is alerted again; the search goes
// on inside 2.5 km.
func TestNoCentreAcknowledgesEscalates(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{}), amended)
	m.run(t0.Add(15 * time.Minute))
	if got := m.forwarded(); len(got) != 2 || !HasEscalation(m.s.Escalations, ReasonForwardingExhausted) ||
		slices.Contains(m.s.CallerNotified, CallerForwarded) {
		t.Fatalf("forwards %v escalations %v caller %v", got, m.s.Escalations, m.s.CallerNotified)
	}
	late := makePool(map[int]int{5: 1})
	m.p = late
	m.run(t0.Add(17 * time.Minute))
	if len(m.alerts(responder.RoleVHT)) != 1 {
		t.Fatal("a responder switching on after forwarding was not alerted")
	}
}

// Study mode: until the amendment, dispatch stays at ring 1; exhausting it
// escalates, and red escalates at 3 minutes without acceptance.
func TestProtocolModeStaysAtRingOne(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{1: 2, 2: 5}), protocol)
	m.run(t0.Add(10 * time.Minute))
	for _, a := range m.s.Alerts {
		if a.Ring != 1 {
			t.Fatalf("alerted at ring %d in protocol mode", a.Ring)
		}
	}
	if len(m.alerts(responder.RoleVHT)) != 2 || !HasEscalation(m.s.Escalations, ReasonExhausted) || m.count(ActForward) != 0 {
		t.Fatalf("alerts %d, escalations %v, forwards %d", len(m.alerts(responder.RoleVHT)), m.s.Escalations, m.count(ActForward))
	}
	// Decision 2 (protocol answer): the coordinator is alerted at 3 minutes
	// for red; the amendment widens the search but keeps that alert.
	for _, par := range []Params{protocol, amended} {
		r := newSim(t, incident.SevRed, makePool(map[int]int{1: 12}), par)
		r.run(t0.Add(2*time.Minute + 59*time.Second))
		if len(r.s.Escalations) != 0 {
			t.Fatalf("%s: red escalated before 3 minutes: %v", par.Mode, r.s.Escalations)
		}
		r.run(t0.Add(3 * time.Minute))
		if !HasEscalation(r.s.Escalations, ReasonRedNoAcceptance) {
			t.Fatalf("%s: red not escalated at 3 minutes: %v", par.Mode, r.s.Escalations)
		}
		for _, a := range r.s.Alerts {
			if par.Mode == ModeProtocol && a.Ring != 1 {
				t.Fatalf("protocol red alerted at ring %d", a.Ring)
			}
		}
	}
}

// An incident with no usable severity is planned as red (rule 3.3: a triage
// error makes the incident red), and one without T2 is not planned at all.
func TestUnknownSeverityIsRedAndNoT2PlansNothing(t *testing.T) {
	m := newSim(t, "", makePool(map[int]int{1: 4}), amended)
	m.run(t0)
	if len(m.alerts(responder.RoleVHT)) != 3 {
		t.Fatalf("unknown severity: %d VHT alerts, want red's 3", len(m.alerts(responder.RoleVHT)))
	}
	m = newSim(t, incident.SevRed, makePool(map[int]int{1: 4}), amended)
	m.s.DispatchedAt = time.Time{}
	if acts, wake := Plan(m.s, m.p, facilities, amended, t0); len(acts) != 0 || !wake.IsZero() {
		t.Fatalf("no T2: %v %v", acts, wake)
	}
}

// Review focus 3: no eligible responder at all escalates immediately in
// protocol mode — dispatch never ends silently.
func TestNoEligibleEscalatesImmediately(t *testing.T) {
	m := newSim(t, incident.SevGreen, makePool(map[int]int{}), protocol)
	m.run(t0)
	if !HasEscalation(m.s.Escalations, ReasonExhausted) {
		t.Fatalf("escalations at T2 = %v", m.s.Escalations)
	}
}

// First acceptance wins per role: the role's other pending alerts are stood
// down, the caller is told help is on the way, and no further alerts go out
// for that role.
func TestAcceptanceStandsDownOthers(t *testing.T) {
	m := newSim(t, incident.SevRed, makePool(map[int]int{1: 6}), amended)
	m.run(t0)
	first := m.alerts(responder.RoleTransporter)[1].ResponderID
	m.accept(first)
	m.run(t0.Add(5 * time.Minute))
	for _, a := range m.alerts(responder.RoleTransporter) {
		if a.ResponderID != first && a.Outcome != OutcomeStoodDown {
			t.Fatalf("transporter alert %v left %s", a.ResponderID, a.Outcome)
		}
	}
	if len(m.alerts(responder.RoleTransporter)) != 3 || !slices.Contains(m.s.CallerNotified, CallerHelperOnTheWay) {
		t.Fatalf("alerts %d, caller %v", len(m.alerts(responder.RoleTransporter)), m.s.CallerNotified)
	}
}

// Review focus 5: while suspended, nothing is alerted and the incident is
// escalated once; on resumption dispatch continues.
func TestSuspendedNeverAlertsAndEscalates(t *testing.T) {
	m := newSim(t, incident.SevRed, makePool(map[int]int{1: 3}), amended)
	m.s.Suspended = true
	m.run(t0.Add(3 * time.Minute))
	if len(m.s.Alerts) != 0 || m.count(ActEscalate) != 1 || !HasEscalation(m.s.Escalations, ReasonSuspended) {
		t.Fatalf("alerts %d, escalations %v", len(m.s.Alerts), m.s.Escalations)
	}
	m.s.Suspended = false
	m.run(t0.Add(3 * time.Minute))
	if len(m.s.Alerts) == 0 {
		t.Fatal("dispatch did not resume")
	}
}

// Review focus 5: suspension switched on mid-cascade — no new alert after the
// switch; the alert in flight may still be accepted.
func TestSuspensionMidCascade(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{1: 4}), amended)
	m.run(t0.Add(100 * time.Second))
	before := len(m.s.Alerts)
	m.s.Suspended = true
	m.run(t0.Add(10 * time.Minute))
	if len(m.s.Alerts) != before || m.count(ActForward) != 0 || !HasEscalation(m.s.Escalations, ReasonSuspended) {
		t.Fatalf("alerts %d→%d, forwards %d, escalations %v", before, len(m.s.Alerts), m.count(ActForward), m.s.Escalations)
	}
	for _, a := range m.s.Alerts {
		if a.Outcome == OutcomePending {
			t.Fatalf("alert %v left pending past its timeout", a.ResponderID)
		}
	}
}

// A decline moves the cascade on at once (it does not wait 90 s).
func TestDeclineMovesCascadeOn(t *testing.T) {
	m := newSim(t, incident.SevGreen, makePool(map[int]int{1: 2}), amended)
	m.react = func(m *sim, a Action) {
		if a.Kind == ActAlert && a.Wave == 1 {
			for i := range m.s.Alerts {
				if m.s.Alerts[i].ResponderID == a.ResponderID {
					m.s.Alerts[i].Outcome = OutcomeDeclined
				}
			}
		}
	}
	m.run(t0)
	if as := m.alerts(responder.RoleVHT); len(as) != 2 || !as[1].SentAt.Equal(t0) {
		t.Fatalf("after a decline: %d alerts", len(as))
	}
}

// Nobody is alerted twice for one incident, and a planner run with nothing
// due returns no actions (idempotent re-planning).
func TestPlanIsIdempotent(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{1: 3}), amended)
	m.run(t0.Add(20 * time.Minute))
	seen := map[uuid.UUID]bool{}
	for _, a := range m.s.Alerts {
		if seen[a.ResponderID] {
			t.Fatalf("%v alerted twice", a.ResponderID)
		}
		seen[a.ResponderID] = true
	}
	acts, _ := Plan(m.s, m.p, facilities, amended, m.now)
	if len(acts) != 0 {
		t.Fatalf("re-plan at the same instant gave %v", acts)
	}
}

// Merged and closed incidents are never planned.
func TestTerminalIncidentsPlanNothing(t *testing.T) {
	m := newSim(t, incident.SevRed, makePool(map[int]int{0: 3}), amended)
	m.s.Done = true
	if acts, wake := Plan(m.s, m.p, facilities, amended, t0); len(acts) != 0 || !wake.IsZero() {
		t.Fatalf("done incident planned %v / %v", acts, wake)
	}
	// Closed with alerts in flight: the responders are stood down.
	m = newSim(t, incident.SevRed, makePool(map[int]int{0: 3}), amended)
	m.run(t0)
	m.s.Done = true
	m.run(t0)
	if m.count(ActStandDown) != 6 {
		t.Fatalf("stand-downs on closure = %d, want 6", m.count(ActStandDown))
	}
}

// Params come from gated config and are validated before the worker starts.
func TestParamsValidate(t *testing.T) {
	if err := amended.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Params){
		"mode":         func(p *Params) { p.Mode = "fast" },
		"interval":     func(p *Params) { p.RingInterval = 0 },
		"max ring":     func(p *Params) { p.MaxRing = 0 },
		"cascade":      func(p *Params) { p.CascadeTimeout = -time.Second },
		"parallel":     func(p *Params) { p.RedParallel = 0 },
		"forward ack":  func(p *Params) { p.ForwardAckTimeout = 0 },
		"red escalate": func(p *Params) { p.RedEscalationAfter = 0 },
	} {
		p := amended
		mut(&p)
		if p.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Review I5: escalations are once per reason AND role — the second role's
// exhaustion is announced too, and red without acceptance names each role.
func TestEscalationsAreOncePerReasonAndRole(t *testing.T) {
	p := makePool(map[int]int{})
	p[responder.RoleTransporter] = []Candidate{{Responder: responder.Responder{ID: uuid.NewV7(), Role: responder.RoleTransporter}, Distance: 1}}
	m := newSim(t, incident.SevYellow, p, protocol)
	m.run(t0)
	if !slices.Contains(m.s.Escalations, incident.EscalationKey(ReasonExhausted, "vht")) || len(m.s.Escalations) != 1 {
		t.Fatalf("at T2: %v", m.s.Escalations)
	}
	m.run(t0.Add(2 * time.Minute))
	if !slices.Contains(m.s.Escalations, incident.EscalationKey(ReasonExhausted, "transporter")) {
		t.Fatalf("the transporter's exhaustion was silent: %v", m.s.Escalations)
	}
	r := newSim(t, incident.SevRed, makePool(map[int]int{1: 12}), protocol)
	r.run(t0.Add(3 * time.Minute))
	for _, role := range []string{"vht", "transporter"} {
		if !slices.Contains(r.s.Escalations, incident.EscalationKey(ReasonRedNoAcceptance, role)) {
			t.Fatalf("red at 3 minutes without %s: %v", role, r.s.Escalations)
		}
	}
}

// Review I9 (review focus 3): in amended mode an empty ring 1 alerts the
// coordinator at T2, once per role, while the search keeps widening.
func TestAmendedEmptyRingOneEscalatesAtOnce(t *testing.T) {
	for _, sev := range []incident.Severity{incident.SevRed, incident.SevYellow} {
		m := newSim(t, sev, makePool(map[int]int{3: 3}), amended)
		m.run(t0)
		for _, role := range []string{"vht", "transporter"} {
			if !slices.Contains(m.s.Escalations, incident.EscalationKey(ReasonNoneNearby, role)) {
				t.Fatalf("%s: empty ring 1 not escalated for %s: %v", sev, role, m.s.Escalations)
			}
		}
		m.run(t0.Add(2*time.Minute + time.Second))
		if len(m.alerts(responder.RoleVHT)) == 0 || HasEscalation(m.s.Escalations, ReasonExhausted) {
			t.Fatalf("%s: the search did not go on: %d alerts, %v", sev, len(m.alerts(responder.RoleVHT)), m.s.Escalations)
		}
	}
	// Protocol mode: ring 1 is the limit, so exhaustion already says it.
	m := newSim(t, incident.SevRed, makePool(map[int]int{3: 3}), protocol)
	m.run(t0)
	if HasEscalation(m.s.Escalations, ReasonNoneNearby) || !HasEscalation(m.s.Escalations, ReasonExhausted) {
		t.Fatalf("protocol mode: %v", m.s.Escalations)
	}
}

// Review I4: a yellow incident raised to red mid-cascade gets its red wave
// (the top 3 of each role) at once; alerts from before the raise do not
// count towards it.
func TestRaisedToRedSendsAFullWave(t *testing.T) {
	m := newSim(t, incident.SevYellow, makePool(map[int]int{1: 6}), amended)
	m.run(t0)
	if len(m.alerts(responder.RoleVHT)) != 1 {
		t.Fatal("yellow at T2: one VHT")
	}
	m.now = t0.Add(30 * time.Second)
	m.s.Severity, m.s.RedSince = incident.SevRed, m.now
	m.run(m.now)
	if n := len(m.alerts(responder.RoleVHT)); n != 4 {
		t.Fatalf("VHT alerts after the raise = %d, want the yellow one plus 3 red", n)
	}
}

// CodeRabbit PR #10 (4557205968): forwards already made are kept up when
// both roles are taken or dispatch is suspended — an unacknowledged one
// times out on schedule, an acknowledgement still tells the caller which
// centre expects them — but no new forward starts. (A closed or merged
// incident accepts no fact, so its forwards stay as they were.)
func TestForwardsSettleWhenDispatchStopsSearching(t *testing.T) {
	for _, stop := range []string{"both roles taken", "suspended"} {
		for _, acked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/acked=%v", stop, acked), func(t *testing.T) {
				m := newSim(t, incident.SevYellow, makePool(map[int]int{}), amended)
				m.run(t0.Add(5 * time.Minute)) // 2.5 km at 5 min: forwarded to the nearest
				if got := m.forwarded(); !slices.Equal(got, []string{"hc_near"}) {
					t.Fatalf("forwards %v", got)
				}
				if stop == "suspended" {
					m.s.Suspended = true
				} else {
					m.s.Assigned[responder.RoleVHT], m.s.Assigned[responder.RoleTransporter] = uuid.NewV7(), uuid.NewV7()
				}
				if acked {
					m.ack("hc_near")
				}
				m.run(t0.Add(15 * time.Minute))
				if got := m.forwarded(); !slices.Equal(got, []string{"hc_near"}) {
					t.Fatalf("a new forward started: %v", got)
				}
				f := m.s.Forwards[0]
				if acked {
					if f.TimedOut || !slices.Contains(m.s.CallerNotified, CallerForwarded) {
						t.Fatalf("acknowledged: %+v, caller %v", f, m.s.CallerNotified)
					}
					return
				}
				if !f.TimedOut || m.now.Before(t0.Add(6*time.Minute)) || slices.Contains(m.s.CallerNotified, CallerForwarded) {
					t.Fatalf("unacknowledged forward not timed out at 6 min: %+v at %v, caller %v", f, m.now, m.s.CallerNotified)
				}
			})
		}
	}
}

// Part C review (rule 14), interim until M3's arrival verification: an
// incident still open FollowUpAfter after its first acceptance is escalated
// once, so a coordinator checks with the caller; it wakes for it; a closed
// incident is not followed up.
func TestFollowUpAfterAcceptance(t *testing.T) {
	t2 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	accepted := t2.Add(2 * time.Minute)
	s := State{Severity: incident.SevYellow, DispatchedAt: t2, AcceptedAt: accepted,
		Assigned:       map[responder.Role]uuid.UUID{responder.RoleVHT: uuid.NewV7(), responder.RoleTransporter: uuid.NewV7()},
		CallerNotified: []string{CallerHelperOnTheWay}}
	acts, wake := Plan(s, pool{}, nil, protocol, accepted.Add(10*time.Minute))
	if len(acts) != 0 || !wake.Equal(accepted.Add(20*time.Minute)) {
		t.Fatalf("before the follow-up: %+v wake %v", acts, wake)
	}
	acts, _ = Plan(s, pool{}, nil, protocol, accepted.Add(20*time.Minute))
	if len(acts) != 1 || acts[0].Kind != ActEscalate || acts[0].Reason != ReasonFollowUp {
		t.Fatalf("at the follow-up: %+v", acts)
	}
	s.Escalations = []string{incident.EscalationKey(ReasonFollowUp, "")}
	if acts, _ := Plan(s, pool{}, nil, protocol, accepted.Add(40*time.Minute)); len(acts) != 0 {
		t.Fatalf("followed up twice: %+v", acts)
	}
	s.Escalations, s.Done = nil, true
	if acts, _ := Plan(s, pool{}, nil, protocol, accepted.Add(40*time.Minute)); len(acts) != 0 {
		t.Fatalf("a closed incident followed up: %+v", acts)
	}
	if err := (Params{Mode: ModeProtocol, RingInterval: time.Minute, MaxRing: 1, CascadeTimeout: time.Minute, RedParallel: 1,
		ForwardAckTimeout: time.Minute, RedEscalationAfter: time.Minute}).Validate(); err == nil {
		t.Fatal("parameters without a follow-up interval accepted")
	}
}
