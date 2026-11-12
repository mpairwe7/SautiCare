//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/responder"
)

// Forwarding (scenarios 7, 8): each forwarded facility gets one SMS
// (dispatch-worker) and one call (ivr-controller), each attempt claimed
// once; a facility that acknowledged or timed out is not called.
func TestForwardAttempts(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	gv := gazetteerVersion(t, r.c)
	if _, err := r.dispatcher.RecordForward(ctx, r.incident, "hc_1", gv, dispatchLogic); err != nil {
		t.Fatal(err)
	}
	att, fresh, err := r.dispatcher.BeginForwardAttempt(ctx, r.incident, "hc_1", dispatch.ChannelSMS)
	if err != nil || !fresh {
		t.Fatalf("sms claim %v %v", fresh, err)
	}
	if _, fresh, _ := r.dispatcher.BeginForwardAttempt(ctx, r.incident, "hc_1", dispatch.ChannelSMS); fresh {
		t.Fatal("facility SMS claimed twice")
	}
	if ok, err := r.dispatcher.FinishForwardAttempt(ctx, att, "sent", "ATXid_f1"); err != nil || !ok {
		t.Fatalf("finish %v %v", ok, err)
	}
	due, err := r.ivr.DueForwardCalls(ctx, 10)
	if err != nil || len(due) != 1 || due[0].IncidentID != r.incident || due[0].FacilityID != "hc_1" {
		t.Fatalf("due forward calls %+v %v", due, err)
	}
	if _, fresh, err := r.ivr.BeginForwardAttempt(ctx, r.incident, "hc_1", dispatch.ChannelVoice); err != nil || !fresh {
		t.Fatalf("voice claim %v %v", fresh, err)
	}
	if due, _ := r.ivr.DueForwardCalls(ctx, 10); len(due) != 0 {
		t.Fatalf("called twice: %+v", due)
	}
	if _, err := r.ivr.AcknowledgeForward(ctx, r.incident, "hc_1"); err != nil {
		t.Fatal(err)
	}
	if _, fresh, _ := r.ivr.BeginForwardAttempt(ctx, r.incident, "hc_2", dispatch.ChannelVoice); fresh {
		t.Fatal("an attempt was claimed for a facility that was never forwarded")
	}
}

// Coordinator escalations by voice (scenario 11): every escalation rings the
// coordinator line, retried until acknowledged; one acknowledgement covers
// the incident's escalations so far, a later escalation rings again.
func TestCoordinatorCalls(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	calls, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(calls) != 1 || calls[0].IncidentID != r.incident || calls[0].Reason != "triage_rule_error" || calls[0].Reference == "" {
		t.Fatalf("calls %+v %v", calls, err)
	}
	if again, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10); len(again) != 0 {
		t.Fatalf("called again before the retry interval: %+v", again)
	}
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.coordinator_calls SET last_call_at = last_call_at - interval '3 minutes'"); err != nil {
		t.Fatal(err)
	}
	retry, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if len(retry) != 1 || retry[0].Calls != 2 {
		t.Fatalf("retry %+v", retry)
	}
	if err := r.ivr.AcknowledgeCoordinatorCall(ctx, r.incident, retry[0].Seq); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.coordinator_calls SET last_call_at = last_call_at - interval '3 minutes'"); err != nil {
		t.Fatal(err)
	}
	if after, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10); len(after) != 0 {
		t.Fatalf("called after acknowledgement: %+v", after)
	}
	if _, err := r.dispatcher.DispatchEscalate(ctx, r.incident, dispatch.ReasonExhausted, responder.RoleVHT, dispatchLogic); err != nil {
		t.Fatal(err)
	}
	if later, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10); len(later) != 1 || later[0].Reason != dispatch.ReasonExhausted {
		t.Fatalf("a later escalation did not ring: %+v", later)
	}
}
