//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
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

// Review I5: the line is rung about the most severe escalation first, then
// the oldest; the claim takes only as many as there are free lines; an
// incident that has closed is not rung about.
func TestCoordinatorCallsBySeverityAndOpenOnly(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "DELETE FROM op.incident_events WHERE type = 'incident.escalated'"); err != nil {
		t.Fatal(err)
	}
	yellow := dispatchedAs(t, r.intake, incident.SevYellow).IncidentID
	red := dispatchedAs(t, r.intake, incident.SevRed).IncidentID
	green := dispatchedAs(t, r.intake, incident.SevGreen).IncidentID
	closed := dispatchedAs(t, r.intake, incident.SevRed).IncidentID
	for _, id := range []uuid.UUID{yellow, red, green, closed} {
		appendEv(t, r.intake, id, incident.EvEscalated, `{"reason":"dispatch_stalled"}`, "")
	}
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.incidents SET state = 'closed' WHERE id = $1", closed); err != nil {
		t.Fatal(err)
	}
	first, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 1)
	if err != nil || len(first) != 1 || first[0].IncidentID != red {
		t.Fatalf("one free line: %+v %v", first, err)
	}
	rest, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if len(rest) != 2 || rest[0].IncidentID != yellow || rest[1].IncidentID != green {
		t.Fatalf("the rest, by severity and without the closed incident: %+v", rest)
	}
}

// CodeRabbit PR #11: a forward that timed out (the request has moved on to
// the next facility) or was acknowledged gets no SMS claim either; the
// facility SMS's delivery report is recorded like any other.
func TestForwardSMSOnlyWhileOpenAndDeliveryReported(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	gv := gazetteerVersion(t, r.c)
	if _, err := r.dispatcher.RecordForward(ctx, r.incident, "hc_1", gv, dispatchLogic); err != nil {
		t.Fatal(err)
	}
	if _, err := r.dispatcher.TimeOutForward(ctx, r.incident, "hc_1"); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := r.dispatcher.BeginForwardAttempt(ctx, r.incident, "hc_1", dispatch.ChannelSMS); err != nil || fresh {
		t.Fatalf("SMS claimed for a timed-out forward: %v %v", fresh, err)
	}
	other := dispatchedIncident(t, r.intake).IncidentID
	if _, err := r.dispatcher.RecordForward(ctx, other, "hc_1", gv, dispatchLogic); err != nil {
		t.Fatal(err)
	}
	att, fresh, err := r.dispatcher.BeginForwardAttempt(ctx, other, "hc_1", dispatch.ChannelSMS)
	if err != nil || !fresh {
		t.Fatalf("SMS claim %v %v", fresh, err)
	}
	if _, err := r.dispatcher.FinishForwardAttempt(ctx, att, "sent", "ATXid_fwd2"); err != nil {
		t.Fatal(err)
	}
	if err := r.intake.RecordDeliveryReport(ctx, opstore.DeliveryReport{ProviderRef: "ATXid_fwd2", Status: "Success", Final: true}); err != nil {
		t.Fatalf("facility SMS delivery report: %v", err)
	}
	var status string
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT delivery_status FROM op.forward_attempts WHERE id = $1", att).Scan(&status); err != nil || status != "Success" {
		t.Fatalf("delivery status %q %v", status, err)
	}
}

// CodeRabbit PR #11: an escalation nobody acknowledged keeps ringing however
// old it is, as long as the incident is open.
func TestUnacknowledgedEscalationsKeepRinging(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"dispatch_stalled"}`, "")
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx,
		"UPDATE op.incident_events SET recorded_at = recorded_at - interval '2 days' WHERE incident_id = $1 AND type = 'incident.escalated'", r.incident); err != nil {
		t.Fatal(err)
	}
	if calls, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10); err != nil || len(calls) != 1 || calls[0].IncidentID != r.incident {
		t.Fatalf("a two-day-old unacknowledged escalation: %+v %v", calls, err)
	}
}
