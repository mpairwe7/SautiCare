package intake

import (
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

var (
	rule  = DedupRule{Window: 15 * time.Minute, MaxZoneDistance: 1}
	base  = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	here  = incident.Zone{Col: 902, Row: 84}
	newID = uuid.NewV7()
)

func cand(category string, zone incident.Zone, t1, t2 time.Time) Candidate {
	return Candidate{ID: uuid.NewV7(), Category: category, Zone: zone, LocationQuality: incident.QualityLandmark, T1: t1, T2: t2}
}

// call is the new call being dispatched (its T2 is being recorded).
func call(category string, zone incident.Zone, t1 time.Time) Candidate {
	return Candidate{ID: newID, Category: category, Zone: zone, LocationQuality: incident.QualityLandmark, T1: t1}
}

// The survivor is the matching incident dispatched earliest: its alerts are
// already out, so the later call joins it rather than restarting dispatch.
func TestSurvivorIsEarliestDispatched(t *testing.T) {
	first := cand("trauma", here, base.Add(2*time.Minute), base.Add(3*time.Minute))
	second := cand("trauma", incident.Zone{Col: 903, Row: 85}, base, base.Add(4*time.Minute))
	got, ok := rule.Survivor(call("trauma", here, base.Add(5*time.Minute)), []Candidate{second, first})
	if !ok || got.ID != first.ID {
		t.Fatalf("survivor = %v %v, want the earliest dispatched", got.ID, ok)
	}
}

func TestNoMergeAcrossCategories(t *testing.T) {
	if _, ok := rule.Survivor(call("trauma", here, base), []Candidate{cand("obstetric", here, base, base)}); ok {
		t.Fatal("merged across categories")
	}
}

func TestNoMergeBeyondOneZone(t *testing.T) {
	far := incident.Zone{Col: here.Col + 2, Row: here.Row}
	if _, ok := rule.Survivor(call("trauma", here, base), []Candidate{cand("trauma", far, base, base)}); ok {
		t.Fatal("merged two zones apart")
	}
	diag := incident.Zone{Col: here.Col + 1, Row: here.Row - 1}
	if _, ok := rule.Survivor(call("trauma", here, base), []Candidate{cand("trauma", diag, base, base)}); !ok {
		t.Fatal("diagonal neighbour (Chebyshev 1) not merged")
	}
}

func TestNoMergeAfter15Minutes(t *testing.T) {
	old := cand("trauma", here, base, base.Add(time.Minute))
	if _, ok := rule.Survivor(call("trauma", here, base.Add(15*time.Minute+time.Second)), []Candidate{old}); ok {
		t.Fatal("merged a call more than 15 minutes later")
	}
	if _, ok := rule.Survivor(call("trauma", here, base.Add(15*time.Minute)), []Candidate{old}); !ok {
		t.Fatal("exactly 15 minutes is within the window")
	}
	if _, ok := rule.Survivor(call("trauma", here, base), []Candidate{cand("trauma", here, base.Add(16*time.Minute), base.Add(17*time.Minute))}); ok {
		t.Fatal("merged with a call 16 minutes later")
	}
}

func TestSurvivorIsNeverTheIncidentItself(t *testing.T) {
	self := Candidate{ID: newID, Category: "trauma", Zone: here, LocationQuality: incident.QualityLandmark, T1: base, T2: base}
	if _, ok := rule.Survivor(call("trauma", here, base), []Candidate{self}); ok {
		t.Fatal("merged into itself")
	}
}

// I1: a call located only to its ward sits at the ward centre whatever its
// real place, so distance says nothing: it is never merged, and no call is
// merged into it. Both are dispatched (fail safe). A matched typed landmark
// (fuzzy) is a real place and merges as a listed one does.
func TestNoMergeAtWardLevel(t *testing.T) {
	wardCand := cand("trauma", here, base, base)
	wardCand.LocationQuality = incident.QualityWard
	if _, ok := rule.Survivor(call("trauma", here, base), []Candidate{wardCand}); ok {
		t.Fatal("merged into a ward-level incident")
	}
	wardCall := call("trauma", here, base)
	wardCall.LocationQuality = incident.QualityWard
	if _, ok := rule.Survivor(wardCall, []Candidate{cand("trauma", here, base, base)}); ok {
		t.Fatal("merged a ward-level call")
	}
	fuzzy := call("trauma", here, base)
	fuzzy.LocationQuality = incident.QualityFuzzy
	if _, ok := rule.Survivor(fuzzy, []Candidate{cand("trauma", here, base, base)}); !ok {
		t.Fatal("a matched typed landmark did not merge")
	}
}

// F-06: a merge never silences an unacknowledged escalation. The survivor
// was escalated and that escalation acknowledged; the absorbed call holds a
// triage rule error nobody has acknowledged. The survivor rings again.
func TestMergedUnacknowledgedEscalationRingsOnTheSurvivor(t *testing.T) {
	survivor := cand("trauma", here, base, base.Add(time.Minute))
	survivor.Escalated = true
	got, err := Consequences(call("trauma", here, base.Add(2*time.Minute)), survivor, incident.ReasonTriageRuleError)
	if err != nil || string(got.Escalate) != `{"reason":"merged_call_escalated"}` {
		t.Fatalf("escalate %s %v", got.Escalate, err)
	}
	// An absorbed call whose escalations were all acknowledged adds none.
	if got, _ := Consequences(call("trauma", here, base.Add(2*time.Minute)), survivor, ""); got.Escalate != nil {
		t.Fatalf("escalated for an acknowledged call: %s", got.Escalate)
	}
}
