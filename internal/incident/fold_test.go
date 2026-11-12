package incident

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
	"uuid"
)

var (
	fixtureID  = uuid.MustParse("0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70")
	fixtureRef = Reference("1234567894")
	t0         = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
)

func ev(seq int64, typ EventType, payload string) Event {
	if payload == "" {
		payload = "{}"
	}
	return Event{
		ID:         uuid.NewV7(),
		IncidentID: fixtureID,
		Seq:        seq,
		Type:       typ,
		RecordedAt: t0.Add(time.Duration(seq) * time.Second),
		ActorKind:  "system",
		Payload:    json.RawMessage(payload),
		IdemKey:    string(typ),
	}
}

// FixtureLifecycle is the M0 fixture: a red incident from call to closure.
func FixtureLifecycle() []Event {
	return []Event{
		ev(1, EvSessionStarted, `{"reference":"1234567894","channel":"ussd_at"}`),
		ev(2, EvScreenAnswered, `{"screen":2,"answer":"1"}`),
		func() Event {
			e := ev(3, EvClassified, `{"severity":"red","category":"trauma"}`)
			e.LogicVersion = "tr-test+sha256:0123456789ab"
			return e
		}(),
		ev(4, EvDispatchReady, ""),
		ev(5, EvAlertIssued, `{"role":"vht"}`),
		ev(6, EvAccepted, `{"role":"vht"}`),
		ev(7, EvArrived, ""),
		ev(8, EvDeparted, ""),
		ev(9, EvAtFacility, ""),
		ev(10, EvClosed, `{"reason":"completed"}`),
	}
}

func TestFoldFixtureLifecycle(t *testing.T) {
	events := FixtureLifecycle()
	wantStates := []State{
		StateIntake, StateIntake, StateTriaged, StateDispatching, StateDispatching,
		StateAssigned, StateOnScene, StateTransporting, StateAtFacility, StateClosed,
	}
	var p Projection
	var err error
	for i, e := range events {
		p, err = Apply(p, e)
		if err != nil {
			t.Fatalf("Apply(%s): %v", e.Type, err)
		}
		if p.State != wantStates[i] {
			t.Fatalf("after %s state = %s, want %s", e.Type, p.State, wantStates[i])
		}
	}
	if p.Version != 10 || p.Reference != fixtureRef || p.Channel != "ussd_at" {
		t.Fatalf("projection = %+v", p)
	}
	if p.Severity != SevRed || p.LogicVersion != "tr-test+sha256:0123456789ab" {
		t.Fatalf("severity/logic = %s/%s", p.Severity, p.LogicVersion)
	}
	// T1..T7 come from session.started, dispatch_ready, alert, accepted,
	// arrived, departed, at_facility respectively.
	wantSeq := map[int]int64{1: 1, 2: 4, 3: 5, 4: 6, 5: 7, 6: 8, 7: 9}
	for k, seq := range wantSeq {
		if p.T[k] == nil || !p.T[k].Equal(events[seq-1].RecordedAt) {
			t.Fatalf("T%d = %v, want %v", k, p.T[k], events[seq-1].RecordedAt)
		}
	}
}

// T-times are first-wins: a second responder's acceptance must not move T4.
func TestTimestampsAreFirstWins(t *testing.T) {
	events := FixtureLifecycle()[:6]
	events = append(events, ev(7, EvAccepted, `{"role":"transporter"}`))
	p, err := Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	if !p.T[4].Equal(events[5].RecordedAt) {
		t.Fatalf("T4 moved to %v", p.T[4])
	}
}

func TestApplyRejectsIllegalTransition(t *testing.T) {
	p, err := Fold(FixtureLifecycle()[:3]) // triaged
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, ev(4, EvDeparted, "")); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("departed in triaged: err = %v, want ErrIllegalTransition", err)
	}
}

// No rule and no model may lower a severity (hard constraint 3).
func TestApplyRejectsSeverityLowering(t *testing.T) {
	p, err := Fold(FixtureLifecycle()[:3]) // classified red
	if err != nil {
		t.Fatal(err)
	}
	p.State = StateIntake // allow a second classification attempt structurally
	if _, err := Apply(p, ev(4, EvClassified, `{"severity":"green"}`)); !errors.Is(err, ErrSeverityLowered) {
		t.Fatalf("lowering red->green: err = %v, want ErrSeverityLowered", err)
	}
}

func TestTerminalStatesRejectStarEvents(t *testing.T) {
	p, err := Fold(FixtureLifecycle())
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []EventType{EvScreenAnswered, EvFlagged, EvEscalated, EvClosed} {
		if _, err := Apply(p, ev(11, typ, `{"flag":"x"}`)); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("%s after closed: err = %v", typ, err)
		}
	}
}

func TestFoldRequiresSessionStartedFirst(t *testing.T) {
	if _, err := Fold(nil); !errors.Is(err, ErrEmptyHistory) {
		t.Fatalf("Fold(nil) = %v", err)
	}
	if _, err := Fold([]Event{ev(1, EvClassified, `{"severity":"red"}`)}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("history not starting with session.started: %v", err)
	}
}

func TestFoldRejectsSequenceGap(t *testing.T) {
	events := FixtureLifecycle()[:3]
	events[2].Seq = 5
	if _, err := Fold(events); !errors.Is(err, ErrSequenceGap) {
		t.Fatalf("gap: err = %v, want ErrSequenceGap", err)
	}
}

func TestFoldIncrementalEqualsBatch(t *testing.T) {
	events := FixtureLifecycle()
	batch, err := Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	var inc Projection
	for _, e := range events {
		if inc, err = Apply(inc, e); err != nil {
			t.Fatal(err)
		}
	}
	bj, _ := json.Marshal(batch)
	ij, _ := json.Marshal(inc)
	if string(bj) != string(ij) {
		t.Fatalf("incremental != batch\n%s\n%s", ij, bj)
	}
}

func TestFlagsEscalationAndMerge(t *testing.T) {
	into := uuid.NewV7()
	events := []Event{
		ev(1, EvSessionStarted, `{"reference":"1234567894","channel":"ivr"}`),
		ev(2, EvFlagged, `{"flag":"needs_location_callback"}`),
		ev(3, EvEscalated, `{"reason":"rule_error"}`),
		ev(4, EvMerged, `{"into":"`+into.String()+`"}`),
	}
	p, err := Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateMerged || !p.Escalated || p.MergedInto == nil || *p.MergedInto != into {
		t.Fatalf("projection = %+v", p)
	}
	if len(p.Flags) != 1 || p.Flags[0] != "needs_location_callback" {
		t.Fatalf("flags = %v", p.Flags)
	}
}

// CodeRabbit PR #3: an incident cannot merge into itself; it would leave
// the workflow with no surviving incident. The SQL write path agrees.
func TestApplyRejectsSelfMerge(t *testing.T) {
	p, err := Fold(FixtureLifecycle()[:1])
	if err != nil {
		t.Fatal(err)
	}
	got, err := Apply(p, ev(2, EvMerged, `{"into":"`+fixtureID.String()+`"}`))
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("self-merge: err = %v, want ErrIllegalTransition", err)
	}
	if got.State != StateIntake || got.MergedInto != nil || got.Version != 1 {
		t.Fatalf("projection changed by a rejected self-merge: %+v", got)
	}
}

func TestTransitionsCoverEveryEventType(t *testing.T) {
	seen := map[EventType]bool{EvSessionStarted: true} // created only by OpenIncident
	for _, tr := range Transitions() {
		seen[tr.Event] = true
	}
	for _, typ := range EventTypes() {
		if !seen[typ] {
			t.Errorf("event type %s has no transition", typ)
		}
	}
}

// CodeRabbit PR #2: an event of another incident must not change this one.
func TestApplyRejectsEventOfAnotherIncident(t *testing.T) {
	p, err := Fold(FixtureLifecycle()[:1])
	if err != nil {
		t.Fatal(err)
	}
	foreign := ev(2, EvClassified, `{"severity":"red"}`)
	foreign.IncidentID = uuid.NewV7()
	got, err := Apply(p, foreign)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("foreign event: err = %v, want ErrIllegalTransition", err)
	}
	if got.Version != 1 || got.Severity != "" {
		t.Fatalf("projection changed by a foreign event: %+v", got)
	}
}
