package incident

import (
	"errors"
	"fmt"
	"testing"
	"time"
	"uuid"
)

func absorbed(seq int64, from uuid.UUID, t1 time.Time) Event {
	return ev(seq, EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":%q}`, from, t1.Format(time.RFC3339Nano)))
}

// Build Prompt §10.3: the earliest call gives T1. A survivor that absorbs a
// call made before its own takes that call's T1.
func TestMergeAbsorbedTakesEarliestT1(t *testing.T) {
	a, b := uuid.NewV7(), uuid.NewV7()
	own := t0.Add(time.Second) // ev(1) records at t0+1s
	earlier, later := own.Add(-3*time.Minute), own.Add(2*time.Minute)
	p, err := Fold([]Event{started(), ev(2, EvClassified, `{"severity":"red"}`), ev(3, EvDispatchReady, `{}`),
		absorbed(4, a, later), absorbed(5, b, earlier)})
	if err != nil {
		t.Fatal(err)
	}
	if !p.T[1].Equal(earlier) || !p.OwnT1.Equal(own) || len(p.Absorbed) != 2 || p.State != StateDispatching {
		t.Fatalf("T1=%v own=%v absorbed=%v state=%s", p.T[1], p.OwnT1, p.Absorbed, p.State)
	}
}

// A wrong merge is split by a coordinator: the member leaves the survivor's
// set and T1 is recomputed from the calls that remain.
func TestSplitRestoresT1(t *testing.T) {
	a, b := uuid.NewV7(), uuid.NewV7()
	own := t0.Add(time.Second)
	ea, eb := own.Add(-5*time.Minute), own.Add(-2*time.Minute)
	split := ev(6, EvSplit, fmt.Sprintf(`{"from":%q}`, a))
	split.ActorKind = ActorStaff
	p, err := Fold([]Event{started(), ev(2, EvClassified, `{"severity":"red"}`), ev(3, EvDispatchReady, `{}`),
		absorbed(4, a, ea), absorbed(5, b, eb), split})
	if err != nil {
		t.Fatal(err)
	}
	if !p.T[1].Equal(eb) || len(p.Absorbed) != 1 {
		t.Fatalf("after splitting a: T1=%v absorbed=%v", p.T[1], p.Absorbed)
	}
	q, err := Apply(p, func() Event { e := ev(7, EvSplit, fmt.Sprintf(`{"from":%q}`, b)); e.ActorKind = ActorStaff; return e }())
	if err != nil || !q.T[1].Equal(own) || len(q.Absorbed) != 0 {
		t.Fatalf("after splitting b: T1=%v absorbed=%v err=%v", q.T[1], q.Absorbed, err)
	}
}

func TestMergeAndSplitValidation(t *testing.T) {
	p, _ := Fold([]Event{started(), ev(2, EvClassified, `{"severity":"red"}`), ev(3, EvDispatchReady, `{}`)})
	member := uuid.NewV7()
	for _, bad := range []Event{
		ev(4, EvMergeAbsorbed, `{"from":"x","t1":"2026-11-12T10:00:00Z"}`),
		ev(4, EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"yesterday"}`, member)),
		ev(4, EvMergeAbsorbed, fmt.Sprintf(`{"from":%q}`, member)),
		ev(4, EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"2026-11-12T10:00:00Z","phone":"x"}`, member)),
		ev(4, EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"2026-11-12T10:00:00Z"}`, fixtureID)),
		ev(4, EvSplit, fmt.Sprintf(`{"from":%q}`, member)),
	} {
		if bad.Type == EvSplit {
			bad.ActorKind = ActorStaff
		}
		if _, err := Apply(p, bad); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s %s: err = %v", bad.Type, bad.Payload, err)
		}
	}
	q, err := Apply(p, absorbed(4, member, t0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(q, absorbed(5, member, t0)); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("absorbing the same call twice: %v", err)
	}
	if _, err := Apply(q, ev(5, EvSplit, fmt.Sprintf(`{"from":%q}`, member))); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("split by a non-staff actor: %v", err)
	}
}

// A member wrongly merged is revived by the split: it returns to dispatching
// (it was merged at T2) and is no longer merged into anything.
func TestUnmergedRevivesMember(t *testing.T) {
	into := uuid.NewV7()
	p, err := Fold([]Event{started(), ev(2, EvClassified, `{"severity":"red"}`), ev(3, EvDispatchReady, `{}`),
		ev(4, EvMerged, fmt.Sprintf(`{"into":%q}`, into)), ev(5, EvUnmerged, fmt.Sprintf(`{"from":%q}`, into))})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateDispatching || p.MergedInto != nil {
		t.Fatalf("state=%s merged_into=%v", p.State, p.MergedInto)
	}
	m, _ := Fold([]Event{started(), ev(2, EvMerged, fmt.Sprintf(`{"into":%q}`, into))})
	if _, err := Apply(m, ev(3, EvUnmerged, fmt.Sprintf(`{"from":%q}`, into))); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("unmerging a member merged before T2: %v", err)
	}
	d, _ := Fold([]Event{started(), ev(2, EvClassified, `{"severity":"red"}`), ev(3, EvDispatchReady, `{}`), ev(4, EvMerged, fmt.Sprintf(`{"into":%q}`, into))})
	if _, err := Apply(d, ev(5, EvUnmerged, fmt.Sprintf(`{"from":%q}`, uuid.NewV7()))); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("unmerged from the wrong survivor: %v", err)
	}
}

// A merged incident accepts its unmerge, and the answers its own caller is
// still giving (dedup happens at T2, before screen 8: every answer is
// persisted, Guide §4). No flag, escalation, closure or resume lands on a
// call that is part of another incident.
func TestMergedAcceptsOnlyItsCallersAnswers(t *testing.T) {
	into := uuid.NewV7()
	p, _ := Fold([]Event{started(), ev(2, EvMerged, fmt.Sprintf(`{"into":%q}`, into))})
	for _, e := range []Event{
		ev(3, EvFlagged, `{"flag":"x"}`), ev(3, EvClosed, `{}`), ev(3, EvEscalated, `{}`),
		ev(3, EvSessionResumed, `{"channel":"ivr"}`), absorbed(3, uuid.NewV7(), t0),
	} {
		if _, err := Apply(p, e); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s on a merged incident: %v", e.Type, err)
		}
	}
	q, err := Apply(p, ev(3, EvScreenAnswered, `{"screen":8,"field":"callback","value":"same_number"}`))
	if err != nil || q.State != StateMerged || q.LastScreen != 8 {
		t.Fatalf("callback answer on a merged incident: %+v %v", q, err)
	}
}

// C2: a call merged into a less urgent survivor raises the survivor's
// severity (it can only rise). severity_raised needs exactly a known
// severity, strictly above the current one, and the merged call it came
// from, which must be in the survivor's absorbed set (a call never absorbed,
// or already split off, raises nothing); it leaves the logic version alone
// and is refused on a merged or closed incident.
func TestSeverityRaised(t *testing.T) {
	from := uuid.NewV7()
	green, err := Fold([]Event{started(), func() Event {
		e := ev(2, EvClassified, `{"severity":"green"}`)
		e.LogicVersion = "tool1-test+sha256:000000000000"
		return e
	}(), ev(3, EvDispatchReady, `{}`), absorbed(4, from, t0)})
	if err != nil {
		t.Fatal(err)
	}
	red, err := Apply(green, ev(5, EvSeverityRaised, fmt.Sprintf(`{"severity":"red","from":%q}`, from)))
	if err != nil || red.Severity != SevRed || red.LogicVersion != green.LogicVersion || red.State != StateDispatching {
		t.Fatalf("raise to red: %+v %v", red, err)
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"severity":"green","from":%q}`, from),  // not higher
		fmt.Sprintf(`{"severity":"purple","from":%q}`, from), // unknown
		`{"severity":"red"}`,                                   // no source
		`{"severity":"red","from":"x"}`,                        // not an ID
		fmt.Sprintf(`{"severity":"red","from":%q}`, fixtureID), // itself
		fmt.Sprintf(`{"severity":"red","from":%q,"why":"x"}`, from),
		fmt.Sprintf(`{"severity":"red","from":%q}`, uuid.NewV7()), // never absorbed
	} {
		if _, err := Apply(green, ev(5, EvSeverityRaised, bad)); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
	if _, err := Apply(red, ev(6, EvSeverityRaised, fmt.Sprintf(`{"severity":"red","from":%q}`, from))); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("raise to the same severity: %v", err)
	}
	split := ev(5, EvSplit, fmt.Sprintf(`{"from":%q}`, from))
	split.ActorKind = ActorStaff
	gone, err := Apply(green, split)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(gone, ev(6, EvSeverityRaised, fmt.Sprintf(`{"severity":"red","from":%q}`, from))); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("raise from a call already split off: %v", err)
	}
	merged, _ := Fold([]Event{started(), ev(2, EvMerged, fmt.Sprintf(`{"into":%q}`, from))})
	closed, _ := Fold([]Event{started(), ev(2, EvClosed, `{}`)})
	for _, p := range []Projection{merged, closed} {
		if _, err := Apply(p, ev(3, EvSeverityRaised, fmt.Sprintf(`{"severity":"red","from":%q}`, uuid.NewV7()))); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("raise in state %s: %v", p.State, err)
		}
	}
}
