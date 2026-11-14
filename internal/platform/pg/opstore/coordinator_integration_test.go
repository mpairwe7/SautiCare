//go:build integration

package opstore_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

// rewind makes every coordinator call due again (the retry interval passed).
func rewind(t *testing.T, r *dispatchRig) {
	t.Helper()
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(context.Background(),
		"UPDATE op.coordinator_calls SET last_call_at = last_call_at - interval '3 minutes'"); err != nil {
		t.Fatal(err)
	}
}

// F-06: one claim announces every reason not yet acknowledged, with its
// role, so an acknowledgement never covers a reason nobody heard; the
// claim's seq is the newest of them.
func TestClaimAnnouncesEveryUnacknowledgedReason(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
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

// F-06: a merge reads, in its transaction, whether the absorbed call holds
// an escalation no coordinator has acknowledged (op_intake and op_ivr).
func TestHasUnacknowledgedEscalation(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "DELETE FROM op.incident_events WHERE type = 'incident.escalated'"); err != nil {
		t.Fatal(err)
	}
	if reason, ok, err := r.intake.HasUnacknowledgedEscalation(ctx, r.incident); err != nil || ok || reason != "" {
		t.Fatalf("none: %q %v %v", reason, ok, err)
	}
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	for _, s := range []*opstore.Store{r.intake, r.ivr} {
		if reason, ok, err := s.HasUnacknowledgedEscalation(ctx, r.incident); err != nil || !ok || reason != "triage_rule_error" {
			t.Fatalf("unacknowledged: %q %v %v", reason, ok, err)
		}
	}
	calls, _ := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	if err := r.ivr.AcknowledgeCoordinatorCall(ctx, r.incident, calls[0].Seq); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.intake.HasUnacknowledgedEscalation(ctx, r.incident); err != nil || ok {
		t.Fatalf("acknowledged: %v %v", ok, err)
	}
}

// A reason recorded for two roles is two things to hear about (UX-3 speaks
// the role): the claim keeps one pair per (reason, role), each with its
// newest event, ordered by that event's seq.
func TestClaimReturnsEachRoleOfAReason(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "DELETE FROM op.incident_events WHERE type = 'incident.escalated'"); err != nil {
		t.Fatal(err)
	}
	appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"no_eligible_responder","role":"vht"}`, "")
	b := appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"reason":"no_eligible_responder","role":"transporter"}`, "")
	calls, err := r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(calls) != 1 || !slices.Equal(calls[0].Reasons, []string{"no_eligible_responder", "no_eligible_responder"}) ||
		!slices.Equal(calls[0].Roles, []string{"vht", "transporter"}) || calls[0].Seq != b.Event.Seq {
		t.Fatalf("claim %+v %v", calls, err)
	}
	// The vht pair again (another escalation for the role): still two pairs,
	// ordered by each pair's newest event.
	c := appendEv(t, r.intake, r.incident, incident.EvEscalated, `{"role":"vht","reason":"no_eligible_responder"}`, "")
	rewind(t, r)
	calls, err = r.ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(calls) != 1 || !slices.Equal(calls[0].Roles, []string{"transporter", "vht"}) || calls[0].Seq != c.Event.Seq {
		t.Fatalf("claim %+v %v", calls, err)
	}
}

// Task 1.2 amendment: the incident of a voice call, from its call ID (the
// IVR session's provider ID), as op_ivr; a USSD session with the same
// provider ID is not a voice call.
func TestIVRCallIncident(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	voice := openParams(t)
	voice.Channel, voice.ProviderSessionID = incident.ChannelIVR, "ch-ivr-1"
	o, err := r.intake.OpenIncident(ctx, voice)
	if err != nil {
		t.Fatal(err)
	}
	ussd := openParams(t)
	ussd.ProviderSessionID = "ch-ussd-1"
	if _, err := r.intake.OpenIncident(ctx, ussd); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := r.ivr.IVRCallIncident(ctx, "ch-ivr-1"); err != nil || !ok || id != o.IncidentID {
		t.Fatalf("voice call: %v %v %v", id, ok, err)
	}
	if _, ok, err := r.ivr.IVRCallIncident(ctx, "ch-ussd-1"); err != nil || ok {
		t.Fatalf("a USSD session taken for a voice call: %v %v", ok, err)
	}
	// Merged into another incident, the call resolves to its survivor, the
	// incident that takes escalations (PR #14 review).
	appendEv(t, r.intake, o.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test+sha256:000000000000")
	appendEv(t, r.intake, o.IncidentID, incident.EvDispatchReady, `{"zone":{"col":902,"row":84},"location_quality":"landmark"}`, "")
	absorb(t, r.intake, incident.Opened{IncidentID: r.incident}, o)
	if id, ok, err := r.ivr.IVRCallIncident(ctx, "ch-ivr-1"); err != nil || !ok || id != r.incident {
		t.Fatalf("merged voice call: %v %v %v, want the survivor %s", id, ok, err, r.incident)
	}
}
