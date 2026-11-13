//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/platform/pg"
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
	hist, err := r.ro.AlertHistoryFor(ctx, tr, 10)
	if err != nil || len(hist) != 1 || hist[0].Outcome != dispatch.OutcomeDeclined || hist[0].DeclineReason != "bike_or_fuel" ||
		hist[0].IncidentID != r.incident {
		t.Fatalf("history %+v %v", hist, err)
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
// already under way (I3); at the first screen the newest pending alert of the
// hour, else the responder's active assignment (C1: an accepted responder
// sees where to go again), else the newest alert of the hour (a retried
// answer finds it). Nothing about other responders.
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
	// A newer alert on another incident, stood down: the pending one is shown.
	other := dispatchedIncident(t, r.intake).IncidentID
	y := r.issue(t, other, vht, responder.RoleVHT)
	if _, err := r.dispatcher.CloseAlert(ctx, y.AlertID, dispatch.OutcomeStoodDown); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := r.intake.MenuAlert(ctx, key(3), vht); got.AlertID != x.AlertID {
		t.Fatalf("newer stood-down alert shown instead of the pending one: %+v", got)
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
	// Accepted and nothing pending: the active assignment, flagged.
	if _, err := r.intake.AcceptAlert(ctx, x.AlertID, vht); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dispatcher.CloseAlert(ctx, z.AlertID, dispatch.OutcomeStoodDown); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := r.intake.MenuAlert(ctx, key(5), vht); !ok || got.AlertID != x.AlertID || !got.Assigned ||
		got.Outcome != dispatch.OutcomeAccepted {
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
