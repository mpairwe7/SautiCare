//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/responder"
)

// Review I5 and M7: an escalation is once per reason and role, and intake's
// escalation key is the dispatch one, so the coordinator is alerted once.
func TestEscalationsKeyedByReasonAndRole(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	for _, role := range []responder.Role{responder.RoleVHT, responder.RoleTransporter, responder.RoleVHT} {
		if _, err := r.dispatcher.DispatchEscalate(ctx, r.incident, dispatch.ReasonExhausted, role, dispatchLogic); err != nil {
			t.Fatal(err)
		}
	}
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"dispatch_suspended"}`, "")
	key := incident.EscalationKey(dispatch.ReasonSuspended, "")
	if _, err := r.intake.Append(ctx, incident.AppendParams{IncidentID: r.incident, Type: incident.EvEscalated,
		Payload: []byte(`{"reason":"dispatch_suspended"}`), ActorKind: incident.ActorSystem, IdemKey: key}); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.dispatcher.DispatchEscalate(ctx, r.incident, dispatch.ReasonSuspended, "", dispatchLogic); err != nil || changed {
		t.Fatalf("dispatch after intake's suspended escalation: changed=%v %v", changed, err)
	}
	snap, err := r.dispatcher.DispatchSnapshot(ctx, r.incident)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{incident.EscalationKey(dispatch.ReasonExhausted, "vht"), incident.EscalationKey(dispatch.ReasonExhausted, "transporter"), key} {
		if !slices.Contains(snap.Escalations, k) {
			t.Errorf("snapshot escalations %v lack %s", snap.Escalations, k)
		}
	}
	var n int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT count(*) FROM op.incident_events WHERE incident_id = $1
		AND type = 'incident.escalated' AND payload ->> 'reason' = 'no_eligible_responder'`, r.incident).Scan(&n); err != nil || n != 2 {
		t.Fatalf("no_eligible_responder events = %d, %v (want one per role)", n, err)
	}
}

func timerGen(t *testing.T, r *dispatchRig, id uuid.UUID) (int64, time.Time) {
	t.Helper()
	var gen int64
	var due time.Time
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(context.Background(),
		"SELECT gen, due_at FROM op.dispatch_timers WHERE incident_id = $1", id).Scan(&gen, &due); err != nil {
		t.Fatal(err)
	}
	return gen, due
}

// Review I4: a red call merged into a yellow incident already dispatching
// wakes its dispatch at once (the red wave goes out now), and the snapshot
// says when the incident became red.
func TestRaiseByMergeWakesDispatch(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	survivor := classifiedAs(t, r.intake, "trauma", incident.SevYellow, false)
	if out, err := dispatchAs(r.intake, survivor, "trauma", incident.SevYellow, false, incident.QualityLandmark, 950); err != nil || out.Merged {
		t.Fatal(out, err)
	}
	if _, err := r.dispatcher.ClaimDueDispatch(ctx, 50, time.Minute); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	before, _ := timerGen(t, r, survivor.IncidentID)
	if err := r.dispatcher.ScheduleDispatch(ctx, survivor.IncidentID, before, &later); err != nil {
		t.Fatal(err)
	}
	red := classifiedAs(t, r.intake, "trauma", incident.SevRed, false)
	if out, err := dispatchAs(r.intake, red, "trauma", incident.SevRed, false, incident.QualityLandmark, 950); err != nil || !out.Merged {
		t.Fatal(out, err)
	}
	gen, due := timerGen(t, r, survivor.IncidentID)
	if gen <= before || due.After(time.Now()) {
		t.Fatalf("after the raise: gen %d (was %d), due %v", gen, before, due)
	}
	snap, _ := r.dispatcher.DispatchSnapshot(ctx, survivor.IncidentID)
	if snap.Severity != incident.SevRed || snap.RedSince == nil || snap.DispatchedAt == nil || !snap.RedSince.After(*snap.DispatchedAt) {
		t.Fatalf("snapshot after the raise: %+v", snap)
	}
}

// Review I1: once dispatch is suspended in the database, no alert is issued
// and no alert attempt is claimed, whatever the worker read before.
func TestNoAlertOrAttemptWhileSuspended(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	if _, err := r.console.SuspendDispatch(ctx, "coordinator_decision", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := r.dispatcher.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelSMS); err != nil || fresh {
		t.Fatalf("alert attempt while suspended: fresh=%v %v", fresh, err)
	}
	_, err := r.dispatcher.IssueAlert(ctx, dispatch.IssueParams{IncidentID: r.incident, ResponderID: r.responder(t, responder.RoleVHT),
		Role: responder.RoleVHT, Ring: 1, Wave: 1, LogicVersion: dispatchLogic})
	if !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("alert while suspended: %v", err)
	}
	if _, err := r.console.ResumeDispatch(ctx, "reviewed", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	// A settled alert is never delivered either.
	if _, err := r.dispatcher.CloseAlert(ctx, a.AlertID, dispatch.OutcomeNoResponse); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := r.dispatcher.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelSMS); err != nil || fresh {
		t.Fatalf("alert attempt for a lapsed alert: fresh=%v %v", fresh, err)
	}
}

// Review I7: closing or merging wakes dispatch (stand-downs are delivered by
// the next pass) instead of forgetting the incident; a merge with alerts in
// flight is refused.
func TestCloseAndMergeWakeDispatch(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	other := dispatchedIncident(t, r.intake).IncidentID
	if _, err := r.intake.Append(ctx, incident.AppendParams{IncidentID: r.incident, Type: incident.EvMerged,
		Payload: []byte(`{"into":"` + other.String() + `"}`), ActorKind: incident.ActorSystem, IdemKey: "merge-busy"}); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("merge with an alert in flight: %v", err)
	}
	if _, err := r.dispatcher.CloseAlert(ctx, a.AlertID, dispatch.OutcomeStoodDown); err != nil {
		t.Fatal(err)
	}
	before, _ := timerGen(t, r, r.incident)
	appendEv(t, r.intake, r.incident, incident.EvClosed, `{"reason":"completed"}`, "")
	gen, due := timerGen(t, r, r.incident)
	if gen <= before || due.After(time.Now()) {
		t.Fatalf("closure did not wake dispatch: gen %d (was %d), due %v", gen, before, due)
	}
	m := dispatchedIncident(t, r.intake).IncidentID
	appendEv(t, r.intake, m, incident.EvMerged, `{"into":"`+other.String()+`"}`, "")
	if _, due := timerGen(t, r, m); due.After(time.Now()) {
		t.Fatalf("merge did not wake dispatch: due %v", due)
	}
}

// PR #8 (4556490881): a call wrongly merged into a survivor that has since
// closed can still be split off — otherwise a separate emergency stays
// undispatched. The survivor stays closed; the call is dispatched.
func TestSplitOffAClosedSurvivor(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	survivor := classifiedAs(t, r.intake, "trauma", incident.SevRed, false)
	if _, err := dispatchAs(r.intake, survivor, "trauma", incident.SevRed, false, incident.QualityLandmark, 960); err != nil {
		t.Fatal(err)
	}
	member := classifiedAs(t, r.intake, "trauma", incident.SevRed, false)
	if out, err := dispatchAs(r.intake, member, "trauma", incident.SevRed, false, incident.QualityLandmark, 960); err != nil || !out.Merged {
		t.Fatal(out, err)
	}
	appendEv(t, r.intake, survivor.IncidentID, incident.EvClosed, `{"reason":"completed"}`, "")
	if err := r.console.SplitIncident(ctx, survivor.IncidentID, member.IncidentID, uuid.NewV7()); err != nil {
		t.Fatalf("split off a closed survivor: %v", err)
	}
	sp, _ := r.ro.Projection(ctx, survivor.IncidentID)
	mp, _ := r.ro.Projection(ctx, member.IncidentID)
	if sp.State != incident.StateClosed || mp.State != incident.StateDispatching || len(sp.Absorbed) != 0 {
		t.Fatalf("survivor %s (absorbed %v), member %s", sp.State, sp.Absorbed, mp.State)
	}
	if _, due := timerGen(t, r, member.IncidentID); due.After(time.Now()) {
		t.Fatal("the split call is not due for dispatch")
	}
	if _, err := r.intake.Append(ctx, incident.AppendParams{IncidentID: survivor.IncidentID, Type: incident.EvFlagged,
		Payload: []byte(`{"flag":"x"}`), ActorKind: incident.ActorSystem, IdemKey: "after-split"}); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("a closed survivor accepted another event: %v", err)
	}
	r.noDrift(t)
}

// Review I3: the snapshot carries the location quality, so a ward-level call
// (whose zone is the ward's centre) is searched from the whole ward.
func TestSnapshotCarriesLocationQuality(t *testing.T) {
	r := newDispatchRig(t)
	o := classifiedAs(t, r.intake, "trauma", incident.SevRed, false)
	if _, err := dispatchAs(r.intake, o, "trauma", incident.SevRed, false, incident.QualityWard, 970); err != nil {
		t.Fatal(err)
	}
	snap, err := r.dispatcher.DispatchSnapshot(context.Background(), o.IncidentID)
	if err != nil || snap.LocationQuality != incident.QualityWard || snap.Zone == nil {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
}

// Review I8: the watchdog's query finds an incident dispatch never touched
// — and only that one.
func TestListStalledDispatch(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	owner := r.c.Owner(t, pg.StoreOperational)
	age := func(id uuid.UUID) {
		if _, err := owner.Exec(ctx, "UPDATE op.incidents SET t2 = t2 - interval '3 minutes' WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
	}
	stalled := r.incident
	age(stalled)
	escalated := dispatchedIncident(t, r.intake).IncidentID
	age(escalated)
	if _, err := r.dispatcher.DispatchEscalate(ctx, escalated, dispatch.ReasonExhausted, responder.RoleVHT, dispatchLogic); err != nil {
		t.Fatal(err)
	}
	alerted := dispatchedIncident(t, r.intake).IncidentID
	r.issue(t, alerted, r.responder(t, responder.RoleVHT), responder.RoleVHT)
	age(alerted)
	dispatchedIncident(t, r.intake) // fresh: not yet
	got, err := r.intake.ListStalledDispatch(ctx, 2*time.Minute)
	if err != nil || len(got) != 1 || got[0] != stalled {
		t.Fatalf("stalled %v %v, want only %v", got, err, stalled)
	}
}
