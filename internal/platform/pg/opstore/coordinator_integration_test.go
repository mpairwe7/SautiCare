//go:build integration

package opstore_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
)

// rewind makes every coordinator call due again (the retry interval passed).
func rewind(t *testing.T, r *dispatchRig) {
	t.Helper()
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(context.Background(),
		"UPDATE op.coordinator_calls SET last_call_at = last_call_at - interval '3 minutes'"); err != nil {
		t.Fatal(err)
	}
}

// F-06: one claim announces every reason not yet acknowledged (each once,
// with the role of its newest event), so an acknowledgement never covers a
// reason nobody heard; the claim's seq is the newest of them.
func TestClaimAnnouncesEveryUnacknowledgedReason(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"no_eligible_responder","role":"transporter"}`, "")
	b := appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"no_eligible_responder","role":"vht"}`, "")
	calls, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls %+v %v", calls, err)
	}
	c := calls[0]
	if !slices.Equal(c.Reasons, []string{"triage_rule_error", "no_eligible_responder"}) || !slices.Equal(c.Roles, []string{"", "vht"}) ||
		c.Seq != b.Event.Seq || c.Severity == "" || c.Reference == "" {
		t.Fatalf("claim %+v, want both reasons and seq %d", c, b.Event.Seq)
	}
	if err := r.ivr.AcknowledgeCoordinatorCall(ctx, r.incident, c.Seq); err != nil {
		t.Fatal(err)
	}
	rewind(t, r)
	if after, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10); len(after) != 0 {
		t.Fatalf("rung after the acknowledgement: %+v", after)
	}
}

// A reason recorded after the claim was not heard in that call: the call's
// acknowledgement does not cover it, and it rings again.
func TestAReasonAfterTheClaimRingsAgain(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	calls, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls %+v %v", calls, err)
	}
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"dispatch_stalled"}`, "")
	if err := r.ivr.AcknowledgeCoordinatorCall(ctx, r.incident, calls[0].Seq); err != nil {
		t.Fatal(err)
	}
	again, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(again) != 1 || !slices.Equal(again[0].Reasons, []string{"dispatch_stalled"}) {
		t.Fatalf("the later reason: %+v %v", again, err)
	}
}

// The dead-man signal (F-01; ADR-0029 rule 8, tier 3): intake-gw, as
// op_intake, reads how many open incidents hold an unacknowledged
// escalation and the age of the oldest, on the database's clock.
func TestOldestUnacknowledgedEscalationGauge(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	owner := r.c.Owner(t, pg.StoreOperational)
	if _, err := owner.Exec(ctx, "DELETE FROM op.incident_events WHERE type = 'incident.escalated'"); err != nil {
		t.Fatal(err)
	}
	if secs, n, err := r.intake.OldestUnacknowledgedEscalation(ctx); err != nil || n != 0 || secs != 0 {
		t.Fatalf("none: %v %d %v", secs, n, err)
	}
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	if _, err := owner.Exec(ctx, `UPDATE op.incident_events SET recorded_at = recorded_at - interval '5 minutes'
		WHERE incident_id = $1 AND type = 'incident.escalated'`, r.incident); err != nil {
		t.Fatal(err)
	}
	secs, n, err := r.intake.OldestUnacknowledgedEscalation(ctx)
	if err != nil || n != 1 || secs < 300 || secs > 400 {
		t.Fatalf("one five minutes old: %v %d %v", secs, n, err)
	}
	calls, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	if err := r.ivr.AcknowledgeCoordinatorCall(ctx, r.incident, calls[0].Seq); err != nil {
		t.Fatal(err)
	}
	if secs, n, err := r.intake.OldestUnacknowledgedEscalation(ctx); err != nil || n != 0 || secs != 0 {
		t.Fatalf("acknowledged: %v %d %v", secs, n, err)
	}
}
