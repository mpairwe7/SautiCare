//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/responder"
)

// Scenario 9's ladder at the store: an alert still pending 20 s after it
// was sent is due a voice call — once (the voice attempt is claimed by
// ivr-controller through the same function), never while dispatch is
// suspended; the answered call finds its alert by the attempt.
func TestDueVoiceAlerts(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	if due, err := r.ivr.DueVoiceAlerts(ctx, 20*time.Second, 10); err != nil || len(due) != 0 {
		t.Fatalf("due before 20 s: %v %v", due, err)
	}
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.alerts SET sent_at = sent_at - interval '25 seconds' WHERE id = $1", a.AlertID); err != nil {
		t.Fatal(err)
	}
	due, err := r.ivr.DueVoiceAlerts(ctx, 20*time.Second, 10)
	if err != nil || len(due) != 1 || due[0].AlertID != a.AlertID || due[0].ResponderID != vht {
		t.Fatalf("due %v %v", due, err)
	}
	if _, err := r.console.SuspendDispatch(ctx, "coordinator_decision", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if due, _ := r.ivr.DueVoiceAlerts(ctx, 20*time.Second, 10); len(due) != 0 {
		t.Fatalf("due while suspended: %v", due)
	}
	if _, err := r.console.ResumeDispatch(ctx, "reviewed", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	att, fresh, err := r.ivr.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelVoice)
	if err != nil || !fresh {
		t.Fatalf("voice attempt: %v %v", fresh, err)
	}
	if due, _ := r.ivr.DueVoiceAlerts(ctx, 20*time.Second, 10); len(due) != 0 {
		t.Fatalf("due after the voice attempt: %v", due)
	}
	got, who, err := r.ivr.AlertForAttempt(ctx, att)
	if err != nil || got.AlertID != a.AlertID || who != vht || got.Facts.Reference == "" || got.Outcome != dispatch.OutcomePending {
		t.Fatalf("alert for attempt %+v %v %v", got, who, err)
	}
	if c, err := r.ivr.ResponderPhone(ctx, vht); err != nil || len(c.Sealed.Ciphertext) == 0 || c.AAD.RowID != vht.String() {
		t.Fatalf("responder phone for the call: %+v %v", c, err)
	}
	if ok, err := r.ivr.FinishAttempt(ctx, att, "sent", "out-1"); err != nil || !ok {
		t.Fatalf("finish %v %v", ok, err)
	}
}

// Review I1 (review focus 4): a responder who switches off, or whose gate
// lapses, after the SMS went out is not rung at 20 s — the alert is not due
// and no attempt can be claimed for it — while a stand-down still goes out.
func TestNoVoiceAlertOnceTheResponderIsNoLongerEligible(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	owner := r.c.Owner(t, pg.StoreOperational)
	age := func(id uuid.UUID) {
		t.Helper()
		if _, err := owner.Exec(ctx, "UPDATE op.alerts SET sent_at = sent_at - interval '25 seconds' WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
	}
	off := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, off, responder.RoleVHT)
	age(a.AlertID)
	if _, err := r.intake.SetAvailability(ctx, off, false, "ussd"); err != nil {
		t.Fatal(err)
	}
	if due, _ := r.ivr.DueVoiceAlerts(ctx, 20*time.Second, 10); len(due) != 0 {
		t.Fatalf("due after switching off: %v", due)
	}
	if _, fresh, err := r.ivr.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelVoice); err != nil || fresh {
		t.Fatalf("voice attempt claimed for a responder off duty: %v %v", fresh, err)
	}
	if _, fresh, err := r.dispatcher.BeginAttempt(ctx, a.AlertID, dispatch.PurposeStandDown, dispatch.ChannelSMS); err != nil || !fresh {
		t.Fatalf("stand-down refused: %v %v", fresh, err)
	}

	lapsed := r.responder(t, responder.RoleTransporter)
	b := r.issue(t, r.incident, lapsed, responder.RoleTransporter)
	age(b.AlertID)
	if _, err := owner.Exec(ctx, "UPDATE op.responders SET insured_until = now() - interval '1 minute' WHERE id = $1", lapsed); err != nil {
		t.Fatal(err)
	}
	if due, _ := r.ivr.DueVoiceAlerts(ctx, 20*time.Second, 10); len(due) != 0 {
		t.Fatalf("due after the insurance lapsed: %v", due)
	}
	if _, fresh, err := r.ivr.BeginAttempt(ctx, b.AlertID, dispatch.PurposeAlert, dispatch.ChannelVoice); err != nil || fresh {
		t.Fatalf("voice attempt claimed after a gate lapsed: %v %v", fresh, err)
	}
}
