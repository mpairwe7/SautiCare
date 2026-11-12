package intake

import (
	"testing"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/triage"
)

// escalateOnDangerYes classifies with the repository table, except that a
// danger yes is a rule error (red, flagged, escalated).
type escalateOnDangerYes struct{ table Classifier }

func (e escalateOnDangerYes) Classify(in triage.Inputs) triage.Result {
	if in.Danger == "yes" {
		return triage.Result(ruleError)
	}
	return e.table.Classify(in)
}

// C2, review reproduction: a red call merged into a green survivor raises
// the survivor to red. The dispatch that is already alerting must not stay
// green (hard constraint 3: severity can only rise).
func TestMergedRedCallRaisesGreenSurvivor(t *testing.T) {
	r := newRig(t, nil, nil)
	a := r.dial(t, "S1", "0772123456")
	a.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "1"} {
		a.send(in)
	}
	if p := a.projection(); p.Severity != incident.SevGreen || p.State != incident.StateDispatching {
		t.Fatalf("first call: %+v", p)
	}
	r.inc.now = r.inc.now.Add(time.Minute)
	b := r.dial(t, "S2", "0772000111")
	b.send("")
	for _, in := range []string{"1", "1", "1", "3", "2", "1", "1"} {
		b.send(in)
	}
	pa, pb := a.projection(), b.projection()
	if pb.State != incident.StateMerged || pb.Severity != incident.SevRed {
		t.Fatalf("second call: %+v", pb)
	}
	if pa.Severity != incident.SevRed || pa.State != incident.StateDispatching {
		t.Fatalf("survivor stayed %s after absorbing a red call", pa.Severity)
	}
}

// C2: an escalated call (here a rule error) merged into a survivor that is
// not escalated escalates the survivor: the coordinator must still be
// alerted, and the merged call accepts nothing but its own answers.
func TestMergedEscalatedCallEscalatesSurvivor(t *testing.T) {
	r := newRig(t, escalateOnDangerYes{repoTriage(t)}, nil)
	a := r.dial(t, "S1", "0772123456")
	a.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "1"} {
		a.send(in)
	}
	r.inc.now = r.inc.now.Add(time.Minute)
	b := r.dial(t, "S2", "0772000111")
	b.send("")
	for _, in := range []string{"1", "1", "1", "3", "2", "1", "1"} {
		b.send(in)
	}
	pa, pb := a.projection(), b.projection()
	if pb.State != incident.StateMerged || !pb.Escalated {
		t.Fatalf("second call: %+v", pb)
	}
	if !pa.Escalated || pa.Severity != incident.SevRed {
		t.Fatalf("survivor: escalated %v, severity %s", pa.Escalated, pa.Severity)
	}
}

// I1, review reproduction: two calls located only to their ward (an
// unmatched typed landmark here; IVR option 6 always) both sit at the ward
// centre, distance 0. They may be unrelated emergencies anywhere in the
// ward: never merge them; both are dispatched (fail safe).
func TestWardLevelCallsAreNotMerged(t *testing.T) {
	r := newRig(t, nil, nil)
	a := r.dial(t, "S1", "0772123456")
	a.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "6", "zzzz qqq"} {
		a.send(in)
	}
	r.inc.now = r.inc.now.Add(5 * time.Minute)
	b := r.dial(t, "S2", "0772000111")
	b.send("")
	for _, in := range []string{"1", "2", "1", "3", "2", "1", "6", "xxxx yyy"} {
		b.send(in)
	}
	pa, pb := a.projection(), b.projection()
	if pa.LocationQuality != incident.QualityWard || pb.LocationQuality != incident.QualityWard {
		t.Fatalf("qualities %s, %s", pa.LocationQuality, pb.LocationQuality)
	}
	if pa.State != incident.StateDispatching || pb.State != incident.StateDispatching {
		t.Fatalf("ward-level calls merged: %s, %s", pa.State, pb.State)
	}
}
