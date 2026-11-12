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
	return Candidate{ID: uuid.NewV7(), Category: category, Zone: zone, T1: t1, T2: t2}
}

// The survivor is the matching incident dispatched earliest: its alerts are
// already out, so the later call joins it rather than restarting dispatch.
func TestSurvivorIsEarliestDispatched(t *testing.T) {
	first := cand("trauma", here, base.Add(2*time.Minute), base.Add(3*time.Minute))
	second := cand("trauma", incident.Zone{Col: 903, Row: 85}, base, base.Add(4*time.Minute))
	got, ok := rule.Survivor(newID, "trauma", here, base.Add(5*time.Minute), []Candidate{second, first})
	if !ok || got != first.ID {
		t.Fatalf("survivor = %v %v, want the earliest dispatched", got, ok)
	}
}

func TestNoMergeAcrossCategories(t *testing.T) {
	if _, ok := rule.Survivor(newID, "trauma", here, base, []Candidate{cand("obstetric", here, base, base)}); ok {
		t.Fatal("merged across categories")
	}
}

func TestNoMergeBeyondOneZone(t *testing.T) {
	far := incident.Zone{Col: here.Col + 2, Row: here.Row}
	if _, ok := rule.Survivor(newID, "trauma", here, base, []Candidate{cand("trauma", far, base, base)}); ok {
		t.Fatal("merged two zones apart")
	}
	diag := incident.Zone{Col: here.Col + 1, Row: here.Row - 1}
	if _, ok := rule.Survivor(newID, "trauma", here, base, []Candidate{cand("trauma", diag, base, base)}); !ok {
		t.Fatal("diagonal neighbour (Chebyshev 1) not merged")
	}
}

func TestNoMergeAfter15Minutes(t *testing.T) {
	old := cand("trauma", here, base, base.Add(time.Minute))
	if _, ok := rule.Survivor(newID, "trauma", here, base.Add(15*time.Minute+time.Second), []Candidate{old}); ok {
		t.Fatal("merged a call more than 15 minutes later")
	}
	if _, ok := rule.Survivor(newID, "trauma", here, base.Add(15*time.Minute), []Candidate{old}); !ok {
		t.Fatal("exactly 15 minutes is within the window")
	}
	if _, ok := rule.Survivor(newID, "trauma", here, base, []Candidate{cand("trauma", here, base.Add(16*time.Minute), base.Add(17*time.Minute))}); ok {
		t.Fatal("merged with a call 16 minutes later")
	}
}

func TestSurvivorIsNeverTheIncidentItself(t *testing.T) {
	self := Candidate{ID: newID, Category: "trauma", Zone: here, T1: base, T2: base}
	if _, ok := rule.Survivor(newID, "trauma", here, base, []Candidate{self}); ok {
		t.Fatal("merged into itself")
	}
}
