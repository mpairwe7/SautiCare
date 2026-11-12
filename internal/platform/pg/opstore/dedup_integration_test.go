//go:build integration

package opstore_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

var dedupRule = intake.DedupRule{Window: 15 * time.Minute, MaxZoneDistance: 1}

// triaged opens an incident with a category and a classification, ready for T2.
func triaged(t *testing.T, s *opstore.Store, category string) incident.Opened {
	t.Helper()
	o, err := s.OpenIncident(context.Background(), openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":3,"field":"category","value":"`+category+`"}`, "")
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test+sha256:000000000000")
	return o
}

func dispatch(s *opstore.Store, o incident.Opened, category string, col int) (intake.DispatchOutcome, error) {
	payload, _ := json.Marshal(map[string]any{"zone": map[string]int{"col": col, "row": 84}, "location_quality": "landmark"})
	return s.DispatchOrMerge(context.Background(), intake.DispatchRequest{IncidentID: o.IncidentID, Payload: payload,
		IdemKey: "dr:" + o.IncidentID.String(), Category: category, Severity: incident.SevRed, LocationQuality: incident.QualityLandmark,
		Zone: incident.Zone{Col: col, Row: 84}, T1: o.T1, Rule: dedupRule})
}

// classifiedAs opens an incident with a category and the given severity,
// escalated if asked, ready for T2.
func classifiedAs(t *testing.T, s *opstore.Store, category string, sev incident.Severity, escalated bool) incident.Opened {
	t.Helper()
	o, err := s.OpenIncident(context.Background(), openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":3,"field":"category","value":"`+category+`"}`, "")
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"`+string(sev)+`"}`, "tool1-test+sha256:000000000000")
	if escalated {
		appendEv(t, s, o.IncidentID, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	}
	return o
}

func dispatchAs(s *opstore.Store, o incident.Opened, category string, sev incident.Severity, escalated bool, quality string, col int) (intake.DispatchOutcome, error) {
	payload, _ := json.Marshal(map[string]any{"zone": map[string]int{"col": col, "row": 84}, "location_quality": quality})
	return s.DispatchOrMerge(context.Background(), intake.DispatchRequest{IncidentID: o.IncidentID, Payload: payload,
		IdemKey: "dr:" + o.IncidentID.String(), Category: category, Severity: sev, Escalated: escalated, LocationQuality: quality,
		Zone: incident.Zone{Col: col, Row: 84}, T1: o.T1, Rule: dedupRule})
}

// C2: a merged call more urgent than its survivor raises the survivor's
// severity (incident.severity_raised, from the merged call) and, if the
// call was escalated, escalates it (merged_call_escalated), in the merge's
// transaction. A less urgent call changes nothing; a retry adds nothing; the
// projections match the events.
func TestMergeCarriesSeverityAndEscalation(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	survivor := classifiedAs(t, s, "trauma", incident.SevGreen, false)
	if out, err := dispatchAs(s, survivor, "trauma", incident.SevGreen, false, incident.QualityLandmark, 902); err != nil || out.Merged {
		t.Fatalf("survivor dispatch: %+v %v", out, err)
	}
	yellow := classifiedAs(t, s, "trauma", incident.SevYellow, false)
	if out, err := dispatchAs(s, yellow, "trauma", incident.SevYellow, false, incident.QualityLandmark, 902); err != nil || !out.Merged {
		t.Fatalf("yellow merge: %+v %v", out, err)
	}
	if p, _ := s.Projection(ctx, survivor.IncidentID); p.Severity != incident.SevYellow || p.Escalated {
		t.Fatalf("after the yellow call: %+v", p)
	}
	red := classifiedAs(t, s, "trauma", incident.SevRed, true)
	for range 2 {
		if out, err := dispatchAs(s, red, "trauma", incident.SevRed, true, incident.QualityFuzzy, 903); err != nil || !out.Merged ||
			out.Survivor != survivor.IncidentID {
			t.Fatalf("red merge: %+v %v", out, err)
		}
	}
	green := classifiedAs(t, s, "trauma", incident.SevGreen, false)
	if out, err := dispatchAs(s, green, "trauma", incident.SevGreen, false, incident.QualityLandmark, 902); err != nil || !out.Merged {
		t.Fatalf("green merge: %+v %v", out, err)
	}
	p, _ := s.Projection(ctx, survivor.IncidentID)
	if p.Severity != incident.SevRed || !p.Escalated || p.State != incident.StateDispatching || len(p.Absorbed) != 3 {
		t.Fatalf("survivor: %+v", p)
	}
	evs, _ := s.Events(ctx, survivor.IncidentID)
	var raised []string
	escalations := 0
	for _, e := range evs {
		switch e.Type {
		case incident.EvSeverityRaised:
			raised = append(raised, string(e.Payload))
		case incident.EvEscalated:
			escalations++
			var pl map[string]string
			if json.Unmarshal(e.Payload, &pl) != nil || len(pl) != 1 || pl["reason"] != "merged_call_escalated" {
				t.Errorf("escalation payload %s", e.Payload)
			}
		}
	}
	if len(raised) != 2 || escalations != 1 {
		t.Fatalf("severity_raised %v, escalations %d", raised, escalations)
	}
	if _, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly")); err != nil || len(drift) != 0 {
		t.Fatalf("drift %v %v", drift, err)
	}
}

// I1 at store level: two calls located only to their ward, same category,
// same zone, minutes apart, are both dispatched; neither is merged.
func TestWardLevelCallsAreNotMerged(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	a, b := classifiedAs(t, s, "trauma", incident.SevRed, false), classifiedAs(t, s, "trauma", incident.SevRed, false)
	for _, o := range []incident.Opened{a, b} {
		if out, err := dispatchAs(s, o, "trauma", incident.SevRed, false, incident.QualityWard, 902); err != nil || out.Merged {
			t.Fatalf("ward-level dispatch: %+v %v", out, err)
		}
	}
	listed := classifiedAs(t, s, "trauma", incident.SevRed, false)
	if out, err := dispatchAs(s, listed, "trauma", incident.SevRed, false, incident.QualityLandmark, 902); err != nil || out.Merged {
		t.Fatalf("a located call merged into a ward-level one: %+v %v", out, err)
	}
}

// Scenario 4 at store level: the later-dispatched call merges into the
// incident already dispatching, and the survivor takes the earlier T1.
func TestDispatchOrMergeMergesIntoEarliestDispatched(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	early := triaged(t, s, "trauma") // first call
	survivor := triaged(t, s, "trauma")
	other := triaged(t, s, "obstetric")
	if out, err := dispatch(s, survivor, "trauma", 902); err != nil || out.Merged {
		t.Fatalf("first dispatch: %+v %v", out, err)
	}
	if out, err := dispatch(s, other, "obstetric", 902); err != nil || out.Merged {
		t.Fatalf("other category merged: %+v %v", out, err)
	}
	out, err := dispatch(s, early, "trauma", 903)
	if err != nil || !out.Merged || out.Survivor != survivor.IncidentID {
		t.Fatalf("second trauma call: %+v %v", out, err)
	}
	sp, _ := s.Projection(ctx, survivor.IncidentID)
	ep, _ := s.Projection(ctx, early.IncidentID)
	if !sp.T[1].Equal(early.T1) || len(sp.Absorbed) != 1 || ep.State != incident.StateMerged || ep.T[2] == nil ||
		ep.MergedInto == nil || *ep.MergedInto != survivor.IncidentID {
		t.Fatalf("survivor %+v\nmember %+v", sp, ep)
	}
	again, err := dispatch(s, early, "trauma", 903)
	if err != nil || !again.Merged || again.Survivor != survivor.IncidentID {
		t.Fatalf("retried dispatch: %+v %v", again, err)
	}
	if _, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly")); err != nil || len(drift) != 0 {
		t.Fatalf("drift %v %v", drift, err)
	}
}

// Review focus 5: two bystanders completing screen 7 at the same moment give
// exactly one survivor; neither call is lost.
func TestConcurrentCompletionsOneSurvivor(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	for round := range 5 {
		a, b := triaged(t, s, "trauma"), triaged(t, s, "trauma")
		var wg sync.WaitGroup
		outs := make([]intake.DispatchOutcome, 2)
		errs := make([]error, 2)
		for i, o := range []incident.Opened{a, b} {
			wg.Go(func() { outs[i], errs[i] = dispatch(s, o, "trauma", 950+round*10) })
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: %v %v", round, errs[0], errs[1])
		}
		if outs[0].Merged == outs[1].Merged {
			t.Fatalf("round %d: merged = %v and %v, want exactly one survivor", round, outs[0].Merged, outs[1].Merged)
		}
	}
}

// A coordinator splits a wrong merge, atomically and logged with the staff
// identity: the member is revived to dispatching and the survivor's T1 is
// recomputed. Only the console role may do it; a retry changes nothing.
func TestSplitIncident(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	early, survivor := triaged(t, s, "trauma"), triaged(t, s, "trauma")
	if _, err := dispatch(s, survivor, "trauma", 902); err != nil {
		t.Fatal(err)
	}
	if out, err := dispatch(s, early, "trauma", 902); err != nil || !out.Merged {
		t.Fatalf("merge: %+v %v", out, err)
	}
	staff := uuid.NewV7()
	if err := s.SplitIncident(ctx, survivor.IncidentID, early.IncidentID, staff); err == nil {
		t.Fatal("op_intake split a merge")
	}
	console := storeAs(t, c, "op_console")
	for range 2 {
		if err := console.SplitIncident(ctx, survivor.IncidentID, early.IncidentID, staff); err != nil {
			t.Fatal(err)
		}
	}
	sp, _ := s.Projection(ctx, survivor.IncidentID)
	ep, _ := s.Projection(ctx, early.IncidentID)
	if !sp.T[1].Equal(survivor.T1) || len(sp.Absorbed) != 0 || ep.State != incident.StateDispatching || ep.MergedInto != nil {
		t.Fatalf("survivor %+v\nmember %+v", sp, ep)
	}
	evs, _ := s.Events(ctx, survivor.IncidentID)
	last := evs[len(evs)-1]
	if last.Type != incident.EvSplit || last.ActorKind != incident.ActorStaff || last.StaffID == nil || *last.StaffID != staff {
		t.Fatalf("split event = %+v", last)
	}
	if err := console.SplitIncident(ctx, survivor.IncidentID, triaged(t, s, "trauma").IncidentID, staff); err == nil {
		t.Fatal("split of a call that was never merged succeeded")
	}
}
