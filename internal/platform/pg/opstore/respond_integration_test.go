//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/responder"
)

// Scenario 10: a decline with reason 3 (bike or fuel problem) is stored,
// linked to the responder, and shown in the responder's history.
func TestScenario10DeclineReasonStoredAndShown(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	tr := r.responder(t, responder.RoleTransporter)
	a := r.issue(t, r.incident, tr, responder.RoleTransporter)
	if out, err := r.intake.DeclineAlert(ctx, a.AlertID, tr, "bike_or_fuel"); err != nil || out != dispatch.OutcomeDeclined {
		t.Fatalf("decline: %s %v", out, err)
	}
	// The history reads as op_readonly and as op_console (CodeRabbit PR #11
	// 4560453650: a column grant, so the console sees the history and no more).
	for role, s := range map[string]*opstore.Store{"op_readonly": r.ro, "op_console": r.console} {
		hist, err := s.AlertHistoryFor(ctx, tr, 10)
		if err != nil || len(hist) != 1 || hist[0].Outcome != dispatch.OutcomeDeclined || hist[0].DeclineReason != "bike_or_fuel" ||
			hist[0].IncidentID != r.incident {
			t.Fatalf("history as %s: %+v %v", role, hist, err)
		}
	}
	if _, err := beginAs(t, r.c, "op_console").Exec(ctx, "SELECT logic_version FROM op.alerts"); err == nil {
		t.Fatal("op_console reads alert columns beyond the history")
	}
	var reason string
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT payload ->> 'reason' FROM op.incident_events
		WHERE incident_id = $1 AND type = 'alert.declined'`, r.incident).Scan(&reason); err != nil || reason != "bike_or_fuel" {
		t.Fatalf("event reason %q %v", reason, err)
	}
	// Free text is refused from the alert's own responder (only codes are
	// stored), and another responder cannot decline the alert at all.
	owner := r.responder(t, responder.RoleTransporter)
	second := r.issue(t, r.incident, owner, responder.RoleTransporter)
	if _, err := r.intake.DeclineAlert(ctx, second.AlertID, owner, "My bike broke"); err == nil {
		t.Fatal("free text accepted as a reason")
	}
	if _, err := r.intake.DeclineAlert(ctx, second.AlertID, tr, "bike_or_fuel"); err == nil {
		t.Fatal("a responder declined another responder's alert")
	}
}

// The responder menu reads the alert its session is about (op_intake): pinned
// at the session's first screen, so a newer alert cannot redirect an answer
// already under way (I3); at the first screen the responder's active
// assignment (C1: an accepted responder sees where to go again, even with an
// alert still pending that they could not take: CodeRabbit PR #11
// 4560453593), else the newest pending alert of the hour, else the newest
// lapsed one still open (0041), else the newest alert of the hour (a retried
// answer finds it). Nothing about other responders. A responder holds one
// pending alert at a time (0042), so each newer alert follows the lapse of
// the one before.
func TestResponderMenuReads(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	key := func(n byte) []byte { b := make([]byte, 32); b[0] = n; return b }
	if _, ok, err := r.intake.MenuAlert(ctx, key(1), vht); err != nil || ok {
		t.Fatalf("no alert yet: %v %v", ok, err)
	}
	x := r.issue(t, r.incident, vht, responder.RoleVHT)
	if _, ok, _ := r.intake.MenuAlert(ctx, key(1), vht); ok {
		t.Fatal("a session pinned to no alert must stay so")
	}
	got, ok, err := r.intake.MenuAlert(ctx, key(2), vht)
	if err != nil || !ok || got.AlertID != x.AlertID || got.IncidentID != r.incident || got.Outcome != dispatch.OutcomePending ||
		got.Facts.Reference == "" || got.Facts.Severity != "red" || got.Role != responder.RoleVHT || got.Assigned {
		t.Fatalf("pending alert %+v %v %v", got, ok, err)
	}
	// x lapses (its role still open), then a newer alert on another incident
	// is stood down: the lapsed one, still acceptable, is shown first.
	if ok, err := r.dispatcher.CloseAlert(ctx, x.AlertID, dispatch.OutcomeNoResponse); err != nil || !ok {
		t.Fatalf("lapse: %v %v", ok, err)
	}
	other := dispatchedIncident(t, r.intake).IncidentID
	y := r.issue(t, other, vht, responder.RoleVHT)
	if _, err := r.dispatcher.CloseAlert(ctx, y.AlertID, dispatch.OutcomeStoodDown); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := r.intake.MenuAlert(ctx, key(3), vht); got.AlertID != x.AlertID || !got.Actionable {
		t.Fatalf("newer stood-down alert shown instead of the lapsed one still open: %+v", got)
	}
	// A newer pending alert: a session under way keeps its alert, a new one sees the newer.
	third := dispatchedIncident(t, r.intake).IncidentID
	z := r.issue(t, third, vht, responder.RoleVHT)
	if got, _, _ := r.intake.MenuAlert(ctx, key(2), vht); got.AlertID != x.AlertID {
		t.Fatalf("pinned session redirected to %+v", got)
	}
	if got, _, _ := r.intake.MenuAlert(ctx, key(4), vht); got.AlertID != z.AlertID {
		t.Fatalf("new session %+v", got)
	}
	// x accepted late (its role still open) while z is pending: a new session
	// shows the active assignment, flagged — z could not be accepted (already
	// assigned).
	if _, err := r.intake.AcceptAlert(ctx, x.AlertID, vht); err != nil {
		t.Fatal(err)
	}
	if out, err := r.intake.AcceptAlert(ctx, z.AlertID, vht); err != nil || out.Result != dispatch.AlreadyAssigned {
		t.Fatalf("the pending alert beside the assignment: %+v %v", out, err)
	}
	if got, ok, _ := r.intake.MenuAlert(ctx, key(5), vht); !ok || got.AlertID != x.AlertID || !got.Assigned ||
		got.Outcome != dispatch.OutcomeAccepted {
		t.Fatalf("assignment with an alert pending: %+v %v", got, ok)
	}
	// A session pinned before the acceptance keeps its alert.
	if got, _, _ := r.intake.MenuAlert(ctx, key(4), vht); got.AlertID != z.AlertID {
		t.Fatalf("pinned session redirected to the assignment: %+v", got)
	}
	if _, err := r.dispatcher.CloseAlert(ctx, z.AlertID, dispatch.OutcomeStoodDown); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := r.intake.MenuAlert(ctx, key(6), vht); !ok || got.AlertID != x.AlertID || !got.Assigned {
		t.Fatalf("after accepting: %+v %v", got, ok)
	}
	// Another responder's session key never reads this responder's pin.
	other2 := r.responder(t, responder.RoleVHT)
	if _, ok, _ := r.intake.MenuAlert(ctx, key(5), other2); ok {
		t.Fatal("a pin read by another responder")
	}
	until, on, err := r.intake.AvailableUntil(ctx, vht)
	if err != nil || !on || time.Until(until) < 7*time.Hour {
		t.Fatalf("availability %v %v %v", until, on, err)
	}
}

// F-26 (0041): an alert that lapsed while its incident still looks for help
// and its role is open is offered on the menu as actionable, and accepting it
// counts, for a VHT and a transporter alike. Once the role is taken, another
// lapsed alert of that role is no longer actionable, and a pending alert is
// still preferred over a lapsed one.
func TestLapsedAlertWithTheRoleOpenIsOfferedOnTheMenu(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	key := func(n byte) []byte { b := make([]byte, 32); b[0], b[1] = 0x41, n; return b }
	v1, v2, tr := r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleTransporter)
	a1, a2, at := r.issue(t, r.incident, v1, responder.RoleVHT), r.issue(t, r.incident, v2, responder.RoleVHT),
		r.issue(t, r.incident, tr, responder.RoleTransporter)
	for _, a := range []dispatch.Issued{a1, a2, at} {
		if ok, err := r.dispatcher.CloseAlert(ctx, a.AlertID, dispatch.OutcomeNoResponse); err != nil || !ok {
			t.Fatalf("lapse: %v %v", ok, err)
		}
	}
	for n, c := range []struct {
		who   uuid.UUID
		alert dispatch.Issued
	}{{v1, a1}, {tr, at}} {
		got, ok, err := r.intake.MenuAlert(ctx, key(byte(n)), c.who)
		if err != nil || !ok || got.AlertID != c.alert.AlertID || got.Outcome != dispatch.OutcomeNoResponse || !got.Actionable || got.Assigned {
			t.Fatalf("lapsed alert, role open: %+v %v %v", got, ok, err)
		}
		if res, err := r.intake.AcceptAlert(ctx, got.AlertID, c.who); err != nil || res.Result != dispatch.Accepted {
			t.Fatalf("late accept: %+v %v", res, err)
		}
	}
	// The VHT role is taken: VHT 2's lapsed alert is shown (newest of the
	// hour) but is not actionable, and accepting it is refused.
	got, ok, err := r.intake.MenuAlert(ctx, key(9), v2)
	if err != nil || !ok || got.AlertID != a2.AlertID || got.Actionable {
		t.Fatalf("lapsed alert, role taken: %+v %v %v", got, ok, err)
	}
	if res, err := r.intake.AcceptAlert(ctx, a2.AlertID, v2); err != nil || res.Result != dispatch.AlreadyTaken {
		t.Fatalf("accept with the role taken: %+v %v", res, err)
	}
	// A pending alert on another incident comes before a lapsed actionable one.
	v3 := r.responder(t, responder.RoleVHT)
	other := dispatchedIncident(t, r.intake).IncidentID
	lapsed := r.issue(t, other, v3, responder.RoleVHT)
	if ok, err := r.dispatcher.CloseAlert(ctx, lapsed.AlertID, dispatch.OutcomeNoResponse); err != nil || !ok {
		t.Fatalf("lapse: %v %v", ok, err)
	}
	third := dispatchedIncident(t, r.intake).IncidentID
	fresh := r.issue(t, third, v3, responder.RoleVHT)
	if got, _, _ := r.intake.MenuAlert(ctx, key(10), v3); got.AlertID != fresh.AlertID || got.Actionable {
		t.Fatalf("pending alert not preferred: %+v", got)
	}
	r.noDrift(t)
}
