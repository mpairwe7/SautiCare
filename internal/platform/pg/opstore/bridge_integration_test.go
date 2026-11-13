//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/responder"
)

// The masked bridge at the store (Build Prompt §10.5): a responder reaches
// the callers of the incidents they hold an open assignment on; a caller,
// recognised by their number's MAC, reaches the responders assigned to their
// open incident. Every bridge call is logged with its times and result —
// identifiers only, never a number (hard constraints 5, 8).
func TestBridgeTargetsAndLog(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	if got, err := r.ivr.BridgeTargetsForResponder(ctx, vht); err != nil || len(got) != 0 {
		t.Fatalf("no assignment yet: %+v %v", got, err)
	}
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	if _, err := r.intake.AcceptAlert(ctx, a.AlertID, vht); err != nil {
		t.Fatal(err)
	}
	got, err := r.ivr.BridgeTargetsForResponder(ctx, vht)
	if err != nil || len(got) != 1 || got[0].IncidentID != r.incident || got[0].Role != responder.RoleVHT || got[0].Reference == "" ||
		got[0].ResponderID != vht {
		t.Fatalf("responder's targets %+v %v", got, err)
	}
	caller := []crypto.MAC{{KeyID: "dev:k:2", Sum: make([]byte, 32)}}
	byCaller, err := r.ivr.BridgeTargetsForCaller(ctx, caller)
	if err != nil || len(byCaller) != 1 || byCaller[0].ResponderID != vht || byCaller[0].IncidentID != r.incident {
		t.Fatalf("caller's targets %+v %v", byCaller, err)
	}
	other := []crypto.MAC{{KeyID: "dev:k:2", Sum: append(make([]byte, 31), 1)}}
	if none, _ := r.ivr.BridgeTargetsForCaller(ctx, other); len(none) != 0 {
		t.Fatalf("another number reached the incident: %+v", none)
	}
	if c, ok, err := r.ivr.CallerContact(ctx, r.incident); err != nil || !ok || len(c.Sealed.Ciphertext) == 0 {
		t.Fatalf("the caller's sealed number for the bridge leg: %+v %v", c, err)
	}

	id, err := r.ivr.BeginBridgeCall(ctx, r.incident, dispatch.BridgeByVHT)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ivr.FinishBridgeCall(ctx, id, dispatch.BridgeConnected); err == nil {
		t.Fatal("connected without being answered")
	}
	if err := r.ivr.BridgeCallAnswered(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := r.ivr.FinishBridgeCall(ctx, id, dispatch.BridgeConnected); err != nil {
		t.Fatal(err)
	}
	if err := r.ivr.FinishBridgeCall(ctx, id, dispatch.BridgeFailed); err != nil {
		t.Fatal(err) // a second end is a no-op
	}
	var result string
	var secs float64
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT result, extract(epoch FROM ended_at - answered_at) FROM op.bridge_calls
		WHERE id = $1`, id).Scan(&result, &secs); err != nil || result != "connected" || secs < 0 {
		t.Fatalf("logged %q %v %v", result, secs, err)
	}
	// Only an incident someone is assigned to can be bridged.
	if _, err := r.ivr.BeginBridgeCall(ctx, dispatchedIncident(t, r.intake).IncidentID, dispatch.BridgeByCaller); err == nil {
		t.Fatal("bridge call begun on an incident with no assignment")
	}
	// Released: the responder no longer reaches the caller.
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.assignments SET released_at = now() WHERE alert_id = $1", a.AlertID); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ivr.BridgeTargetsForResponder(ctx, vht); len(got) != 0 {
		t.Fatalf("released assignment still bridged: %+v", got)
	}
}

// CodeRabbit PR #12: a second caller's report merged into a survivor keeps
// its session on the member, yet the responders come to the survivor. That
// caller reaches the survivor's responders; once a coordinator splits the
// merge, they no longer do.
func TestBridgeCallerOfAMergedReport(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	if _, err := r.intake.AcceptAlert(ctx, a.AlertID, vht); err != nil {
		t.Fatal(err)
	}
	neighbour := crypto.MAC{KeyID: "dev:k:2", Sum: append(make([]byte, 31), 7)}
	p := openParams(t)
	p.PhoneMAC = neighbour
	member, err := r.intake.OpenIncident(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, r.intake, member.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test+sha256:000000000000")
	appendEv(t, r.intake, member.IncidentID, incident.EvDispatchReady, `{"zone":{"col":902,"row":84},"location_quality":"landmark"}`, "")
	if got, err := r.ivr.BridgeTargetsForCaller(ctx, []crypto.MAC{neighbour}); err != nil || len(got) != 0 {
		t.Fatalf("before the merge, their own report has nobody assigned: %+v %v", got, err)
	}
	absorb(t, r.intake, incident.Opened{IncidentID: r.incident}, member)
	got, err := r.ivr.BridgeTargetsForCaller(ctx, []crypto.MAC{neighbour})
	if err != nil || len(got) != 1 || got[0].IncidentID != r.incident || got[0].ResponderID != vht {
		t.Fatalf("the merged report's caller reaches the survivor's responder: %+v %v", got, err)
	}
	if err := r.console.SplitIncident(ctx, r.incident, member.IncidentID, uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ivr.BridgeTargetsForCaller(ctx, []crypto.MAC{neighbour}); err != nil || len(got) != 0 {
		t.Fatalf("split off, their report has nobody assigned again: %+v %v", got, err)
	}
}
