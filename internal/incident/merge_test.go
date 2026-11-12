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
