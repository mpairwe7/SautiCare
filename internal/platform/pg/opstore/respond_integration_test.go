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
	if _, err := r.intake.DeclineAlert(ctx, r.issue(t, r.incident, r.responder(t, responder.RoleTransporter), responder.RoleTransporter).AlertID,
		tr, "My bike broke"); err == nil {
		t.Fatal("free text accepted as a reason (or another responder's alert)")
	}
}

// The responder menu reads the responder's own most recent alert (within
// the hour, whatever its outcome, so a retried answer finds the same alert)
// and current availability (op_intake), nothing about other responders.
func TestResponderMenuReads(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	if _, ok, err := r.intake.RecentAlertFor(ctx, vht); err != nil || ok {
		t.Fatalf("no alert yet: %v %v", ok, err)
	}
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	got, ok, err := r.intake.RecentAlertFor(ctx, vht)
	if err != nil || !ok || got.AlertID != a.AlertID || got.IncidentID != r.incident || got.Outcome != dispatch.OutcomePending ||
		got.Facts.Reference == "" || got.Facts.Severity != "red" || got.Role != responder.RoleVHT {
		t.Fatalf("recent alert %+v %v %v", got, ok, err)
	}
	until, on, err := r.intake.AvailableUntil(ctx, vht)
	if err != nil || !on || time.Until(until) < 7*time.Hour {
		t.Fatalf("availability %v %v %v", until, on, err)
	}
	if _, err := r.intake.AcceptAlert(ctx, a.AlertID, vht); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := r.intake.RecentAlertFor(ctx, vht); !ok || got.Outcome != dispatch.OutcomeAccepted {
		t.Fatalf("after accepting: %+v %v", got, ok)
	}
}
