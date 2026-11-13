//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/responder"
)

const dispatchLogic = "default-2026-11-12+sha256:0123456789ab"

// dispatchRig is an operational store with a dispatched red incident and the
// stores of the roles that write dispatch facts.
type dispatchRig struct {
	c                           *pgtest.TestCluster
	ids                         crypto.Identifiers
	intake, dispatcher, ivr, ro *opstore.Store
	console                     *opstore.Store
	incident                    uuid.UUID
	phone                       int
}

func newDispatchRig(t *testing.T) *dispatchRig {
	t.Helper()
	c := pgtest.Cluster(t)
	gazetteerVersion(t, c) // responders register at a stage of the gazetteer in force
	r := &dispatchRig{c: c, ids: identifiers(t), intake: storeAs(t, c, "op_intake"), dispatcher: storeAs(t, c, "op_dispatch"),
		ivr: storeAs(t, c, "op_ivr"), ro: storeAs(t, c, "op_readonly"), console: storeAs(t, c, "op_console")}
	r.incident = dispatchedIncident(t, r.intake).IncidentID
	return r
}

// responder registers an eligible, available responder of role.
func (r *dispatchRig) responder(t *testing.T, role responder.Role) uuid.UUID {
	t.Helper()
	r.phone++
	phone, err := pii.ParsePhone("07720" + string(rune('0'+r.phone/10)) + string(rune('0'+r.phone%10)) + "999")
	if err != nil {
		t.Fatal(err)
	}
	cert, ins := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	reg, err := responder.NewService(r.console, r.ids).Register(context.Background(), responder.RegisterCmd{Role: role,
		Name: pii.NewPersonName("Synthetic"), Phone: phone, Ward: "w01", Stage: "w01_stage", CertifiedAt: &cert, InsuredUntil: &ins, KitOK: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.intake.SetAvailability(context.Background(), reg.ID, true, "ussd"); err != nil {
		t.Fatal(err)
	}
	return reg.ID
}

func (r *dispatchRig) issue(t *testing.T, incidentID, resp uuid.UUID, role responder.Role) dispatch.Issued {
	t.Helper()
	is, err := r.dispatcher.IssueAlert(context.Background(), dispatch.IssueParams{IncidentID: incidentID, ResponderID: resp,
		Role: role, Ring: 1, Wave: 1, LogicVersion: dispatchLogic})
	if err != nil {
		t.Fatal(err)
	}
	return is
}

func (r *dispatchRig) noDrift(t *testing.T) {
	t.Helper()
	if _, drift, err := opstore.VerifyProjections(context.Background(), r.ro); err != nil || len(drift) != 0 {
		t.Fatalf("projection drift %v %v", drift, err)
	}
}

// Review focus 2: responders accepting the same role at the same instant —
// exactly one assignment; every other hears "already taken".
func TestConcurrentAcceptsOneAssignment(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	var alerts, resps []uuid.UUID
	for range 6 {
		id := r.responder(t, responder.RoleTransporter)
		resps = append(resps, id)
		alerts = append(alerts, r.issue(t, r.incident, id, responder.RoleTransporter).AlertID)
	}
	results := make([]dispatch.AcceptOutcome, len(alerts))
	errs := make([]error, len(alerts))
	var wg sync.WaitGroup
	for i := range alerts {
		wg.Go(func() { results[i], errs[i] = r.intake.AcceptAlert(ctx, alerts[i], resps[i]) })
	}
	wg.Wait()
	won := 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("accept %d: %v", i, errs[i])
		}
		switch res.Result {
		case dispatch.Accepted:
			won++
		case dispatch.AlreadyTaken:
		default:
			t.Fatalf("accept %d: %s", i, res.Result)
		}
	}
	snap, err := r.dispatcher.DispatchSnapshot(ctx, r.incident)
	if err != nil || won != 1 || len(snap.Assigned) != 1 || snap.State != incident.StateAssigned {
		t.Fatalf("won %d, assigned %v, state %s, %v", won, snap.Assigned, snap.State, err)
	}
	p, _ := r.ro.Projection(ctx, r.incident)
	if p.T[3] == nil || p.T[4] == nil || p.T[4].Before(*p.T[3]) {
		t.Fatalf("T3 %v T4 %v", p.T[3], p.T[4])
	}
	// A retried accept by the winner is still "accepted" and adds nothing.
	for i, res := range results {
		if res.Result == dispatch.Accepted {
			again, err := r.intake.AcceptAlert(ctx, alerts[i], resps[i])
			if err != nil || again.Result != dispatch.Accepted {
				t.Fatalf("retry: %v %v", again, err)
			}
		}
	}
	r.noDrift(t)
}

// The database refuses an alert the planner should never send (review focus
// 4 and the merged-call rule): defence in depth.
func TestIssueAlertGuards(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	vht := r.responder(t, responder.RoleVHT)
	first := r.issue(t, r.incident, vht, responder.RoleVHT)
	again := r.issue(t, r.incident, vht, responder.RoleVHT)
	if first.Duplicate || !again.Duplicate || again.AlertID != first.AlertID {
		t.Fatalf("one alert per responder per incident: %+v %+v", first, again)
	}
	p, _ := r.ro.Projection(ctx, r.incident)
	if p.T[3] == nil || !p.T[3].Equal(first.SentAt) {
		t.Fatalf("T3 = %v, first alert at %v", p.T[3], first.SentAt)
	}

	refused := func(name string, inc, resp uuid.UUID, role responder.Role) {
		t.Helper()
		_, err := r.dispatcher.IssueAlert(ctx, dispatch.IssueParams{IncidentID: inc, ResponderID: resp, Role: role, Ring: 1, Wave: 1, LogicVersion: dispatchLogic})
		if !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("%s: err = %v, want refused", name, err)
		}
	}
	off := r.responder(t, responder.RoleVHT)
	if _, err := r.intake.SetAvailability(ctx, off, false, "ussd"); err != nil {
		t.Fatal(err)
	}
	refused("availability off", r.incident, off, responder.RoleVHT)
	noKit := r.responder(t, responder.RoleVHT)
	if err := r.console.SetGates(ctx, noKit, responder.Gates{CertifiedAt: ptr(time.Now().Add(-time.Hour)), InsuredUntil: ptr(time.Now().Add(time.Hour)),
		KitOK: false, Status: responder.StatusActive}); err != nil {
		t.Fatal(err)
	}
	refused("kit not OK", r.incident, noKit, responder.RoleVHT)
	refused("role mismatch", r.incident, r.responder(t, responder.RoleTransporter), responder.RoleVHT)

	if res, err := r.intake.AcceptAlert(ctx, first.AlertID, vht); err != nil || res.Result != dispatch.Accepted {
		t.Fatalf("accept: %v %v", res, err)
	}
	refused("role taken", r.incident, r.responder(t, responder.RoleVHT), responder.RoleVHT)
	other := dispatchedIncident(t, r.intake).IncidentID
	refused("assigned elsewhere", other, vht, responder.RoleVHT)

	merged := dispatchedIncident(t, r.intake).IncidentID
	appendEv(t, r.intake, merged, incident.EvMerged, `{"into":"`+other.String()+`"}`, "")
	refused("merged incident", merged, r.responder(t, responder.RoleTransporter), responder.RoleTransporter)
	if _, err := r.dispatcher.IssueAlert(ctx, dispatch.IssueParams{IncidentID: other, ResponderID: r.responder(t, responder.RoleTransporter),
		Role: responder.RoleTransporter, Ring: 1, Wave: 1}); err == nil {
		t.Error("an alert without the dispatch parameter version was accepted")
	}
	r.noDrift(t)
}

func ptr[T any](v T) *T { return &v }

// Dispatch facts are written only by their functions: the generic append
// refuses them, and each role reaches only its own commands.
func TestDispatchFactsOnlyThroughTheirFunctions(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	// Well-formed payloads: only the dispatch-functions rule refuses them.
	ids := `"alert":"` + uuid.NewV7().String() + `","responder":"` + uuid.NewV7().String() + `"`
	valid := map[incident.EventType]string{
		incident.EvAlertIssued:         `{` + ids + `,"role":"vht","ring":1,"wave":1}`,
		incident.EvAccepted:            `{` + ids + `,"role":"vht"}`,
		incident.EvAlertDeclined:       `{` + ids + `}`,
		incident.EvAlertNoResponse:     `{"alert":"` + uuid.NewV7().String() + `"}`,
		incident.EvAlertStoodDown:      `{"alert":"` + uuid.NewV7().String() + `"}`,
		incident.EvForwarded:           `{"facility":"hc_1"}`,
		incident.EvForwardAcknowledged: `{"facility":"hc_1"}`,
		incident.EvForwardTimedOut:     `{"facility":"hc_1"}`,
		incident.EvCallerNotified:      `{"kind":"helper_on_the_way"}`,
	}
	for _, typ := range incident.DispatchEvents() {
		if valid[typ] == "" {
			t.Fatalf("no valid payload for %s", typ)
		}
		_, err := r.intake.Append(ctx, incident.AppendParams{IncidentID: r.incident, Type: typ, Payload: []byte(valid[typ]),
			ActorKind: incident.ActorSystem, IdemKey: "direct:" + string(typ)})
		if !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("generic append of %s: %v", typ, err)
		}
	}
	vht := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	for role, q := range map[string]string{
		"op_intake":   "SELECT op.issue_alert('" + r.incident.String() + "', '" + vht.String() + "', 'vht', 1, 1, '" + dispatchLogic + "')",
		"op_dispatch": "SELECT op.accept_alert('" + a.AlertID.String() + "', '" + vht.String() + "')",
		"op_console":  "SELECT op.close_alert('" + a.AlertID.String() + "', 'stood_down')",
		"op_ivr":      "UPDATE op.alerts SET outcome = 'accepted'",
		"op_readonly": "DELETE FROM op.assignments",
	} {
		conn, err := pg.Connect(ctx, r.c.DSN(pg.StoreOperational, role), "test", 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("%s: %s: err = %v, want permission denied", role, q, err)
		}
		conn.Close()
	}
	other := r.responder(t, responder.RoleVHT)
	if _, err := r.intake.AcceptAlert(ctx, a.AlertID, other); err == nil {
		t.Error("a responder accepted another responder's alert")
	}
}

// Every dispatch fact lands in its table and the log in one transaction,
// the projection still equals the fold, and closing with a responder on the
// way is refused. The dispatch logic version is on every fact the planner
// decides and on none that follows from one (acknowledgement, forward
// timeout, caller notice): exactly what governance/ai_risk_manifest.yaml
// claims (CodeRabbit PR #10).
func TestDispatchLifecycleIsRecordedAndConsistent(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	v1, v2, tr := r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleVHT), r.responder(t, responder.RoleTransporter)
	a1 := r.issue(t, r.incident, v1, responder.RoleVHT)
	if out, err := r.intake.DeclineAlert(ctx, a1.AlertID, v1, ""); err != nil || out != "declined" {
		t.Fatalf("decline: %s %v", out, err)
	}
	if out, _ := r.intake.DeclineAlert(ctx, a1.AlertID, v1, ""); out != "declined" {
		t.Fatalf("decline retry: %s", out)
	}
	a2 := r.issue(t, r.incident, v2, responder.RoleVHT)
	if ok, err := r.dispatcher.CloseAlert(ctx, a2.AlertID, dispatch.OutcomeNoResponse); err != nil || !ok {
		t.Fatalf("no response: %v %v", ok, err)
	}
	// A late acceptance after the lapse counts while the role is open.
	if res, err := r.intake.AcceptAlert(ctx, a2.AlertID, v2); err != nil || res.Result != dispatch.Accepted {
		t.Fatalf("late accept: %v %v", res, err)
	}
	a3 := r.issue(t, r.incident, tr, responder.RoleTransporter)
	if _, err := r.intake.Append(ctx, incident.AppendParams{IncidentID: r.incident, Type: incident.EvClosed,
		Payload: []byte(`{"reason":"completed"}`), ActorKind: incident.ActorSystem, IdemKey: "close-early"}); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("closed with a responder on the way: %v", err)
	}
	if ok, err := r.dispatcher.CloseAlert(ctx, a3.AlertID, dispatch.OutcomeStoodDown); err != nil || !ok {
		t.Fatalf("stand down: %v %v", ok, err)
	}
	if ok, _ := r.dispatcher.CloseAlert(ctx, a3.AlertID, dispatch.OutcomeNoResponse); ok {
		t.Fatal("a settled alert was settled again")
	}
	gv := gazetteerVersion(t, r.c)
	if ok, err := r.dispatcher.RecordForward(ctx, r.incident, "hc_1", gv, dispatchLogic); err != nil || !ok {
		t.Fatalf("forward: %v %v", ok, err)
	}
	if _, err := r.dispatcher.RecordForward(ctx, r.incident, "hc_2", gv, dispatchLogic); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("forward to a facility without forwarding: %v", err)
	}
	if ok, err := r.dispatcher.TimeOutForward(ctx, r.incident, "hc_1"); err != nil || !ok {
		t.Fatalf("forward timeout: %v %v", ok, err)
	}
	if ok, err := r.ivr.AcknowledgeForward(ctx, r.incident, "hc_1"); err != nil || !ok {
		t.Fatalf("late acknowledgement: %v %v", ok, err)
	}
	for _, kind := range []string{dispatch.CallerHelperOnTheWay, dispatch.CallerHelperOnTheWay} {
		if _, err := r.dispatcher.NotifyCaller(ctx, r.incident, kind, ""); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := r.dispatcher.NotifyCaller(ctx, r.incident, dispatch.CallerForwarded, "hc_1"); err != nil || !ok {
		t.Fatalf("forward notice: %v %v", ok, err)
	}
	for range 2 {
		if _, err := r.dispatcher.DispatchEscalate(ctx, r.incident, dispatch.ReasonForwarded, responder.RoleTransporter, dispatchLogic); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := r.dispatcher.DispatchSnapshot(ctx, r.incident)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[uuid.UUID]string{}
	for _, a := range snap.Alerts {
		outcomes[a.ID] = a.Outcome
	}
	if outcomes[a1.AlertID] != dispatch.OutcomeDeclined || outcomes[a2.AlertID] != dispatch.OutcomeAccepted ||
		outcomes[a3.AlertID] != dispatch.OutcomeStoodDown || snap.Assigned[responder.RoleVHT] != v2 ||
		len(snap.Forwards) != 1 || !snap.Forwards[0].Acked || !snap.Forwards[0].TimedOut ||
		len(snap.Escalations) != 1 || snap.Escalations[0] != incident.EscalationKey(dispatch.ReasonForwarded, "transporter") || len(snap.CallerNotified) != 2 ||
		snap.DispatchedAt == nil || snap.Zone == nil || snap.Severity != incident.SevRed {
		t.Fatalf("snapshot %+v", snap)
	}
	var events int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT count(*) FROM op.incident_events WHERE incident_id = $1 AND type = 'caller.notified'",
		r.incident).Scan(&events); err != nil || events != 2 {
		t.Fatalf("caller.notified events = %d, %v", events, err)
	}
	rows, err := r.c.Owner(t, pg.StoreOperational).Query(ctx, `SELECT type, bool_and(logic_version IS NOT DISTINCT FROM $2), bool_and(logic_version IS NULL)
		FROM op.incident_events WHERE incident_id = $1 AND (type LIKE 'alert.%' OR type IN ('responder.accepted', 'incident.forwarded',
		     'incident.forward_acknowledged', 'incident.forward_timed_out', 'caller.notified', 'incident.escalated'))
		GROUP BY type`, r.incident, dispatchLogic)
	if err != nil {
		t.Fatal(err)
	}
	versioned := map[string]bool{"alert.issued": true, "responder.accepted": true, "alert.declined": true, "alert.no_response": true,
		"alert.stood_down": true, "incident.forwarded": true, "incident.escalated": true,
		"incident.forward_acknowledged": false, "incident.forward_timed_out": false, "caller.notified": false}
	seen := 0
	for rows.Next() {
		var typ string
		var all, none bool
		if err := rows.Scan(&typ, &all, &none); err != nil {
			t.Fatal(err)
		}
		if want, ok := versioned[typ]; !ok || (want && !all) || (!want && !none) {
			t.Errorf("%s: logic version on all %v, on none %v", typ, all, none)
		}
		seen++
	}
	if rows.Err() != nil || seen != len(versioned) {
		t.Fatalf("fact types seen %d of %d: %v", seen, len(versioned), rows.Err())
	}
	r.noDrift(t)
}

// The planner's wake: T2 makes the incident due; a claim leases it; a new
// fact after the claim keeps it due however the stale plan schedules it; a
// merge ends it.
func TestDispatchTimers(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	due, err := r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute)
	if err != nil || len(due) != 1 || due[0].IncidentID != r.incident {
		t.Fatalf("claim after T2: %v %v", due, err)
	}
	if again, _ := r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute); len(again) != 0 {
		t.Fatalf("a leased incident was claimed twice: %v", again)
	}
	vht := r.responder(t, responder.RoleVHT)
	a := r.issue(t, r.incident, vht, responder.RoleVHT)
	if _, err := r.intake.AcceptAlert(ctx, a.AlertID, vht); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := r.dispatcher.ScheduleDispatch(ctx, r.incident, due[0].Gen, &later); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute); len(again) != 1 || again[0].Gen <= due[0].Gen {
		t.Fatalf("a fact after the claim was lost by a stale schedule: %v", again)
	} else if err := r.dispatcher.ScheduleDispatch(ctx, r.incident, again[0].Gen, nil); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute); len(again) != 0 {
		t.Fatalf("nothing scheduled, yet claimed: %v", again)
	}
	// Most urgent first.
	green := classifiedAs(t, r.intake, "trauma", incident.SevGreen, false)
	appendEv(t, r.intake, green.IncidentID, incident.EvDispatchReady, `{"zone":{"col":902,"row":84},"location_quality":"landmark"}`, "")
	red := dispatchedIncident(t, r.intake)
	got, _ := r.dispatcher.ClaimDueDispatch(ctx, 1, time.Minute)
	if len(got) != 1 || got[0].IncidentID != red.IncidentID {
		t.Fatalf("claimed %v first, want the red incident", got)
	}
	// A merge wakes the merged call once (review I7: anything in flight is
	// stood down and delivered); the planner then parks it for good.
	appendEv(t, r.intake, green.IncidentID, incident.EvMerged, `{"into":"`+red.IncidentID.String()+`"}`, "")
	got, _ = r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute)
	if i := slices.IndexFunc(got, func(d dispatch.Due) bool { return d.IncidentID == green.IncidentID }); i < 0 {
		t.Fatalf("the merge did not wake the merged call: %v", got)
	} else if err := r.dispatcher.ScheduleDispatch(ctx, green.IncidentID, got[i].Gen, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute); slices.ContainsFunc(got, func(d dispatch.Due) bool { return d.IncidentID == green.IncidentID }) {
		t.Fatalf("a parked merged call is due again: %v", got)
	}
}

// Review focus 1: an attempt is claimed before the provider call and only
// once, so a worker that crashed after claiming never sends it again.
func TestAttemptsAreClaimedOnce(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	a := r.issue(t, r.incident, r.responder(t, responder.RoleVHT), responder.RoleVHT)
	id, fresh, err := r.dispatcher.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelSMS)
	if err != nil || !fresh {
		t.Fatalf("first claim: %v %v", fresh, err)
	}
	id2, fresh, err := r.dispatcher.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelSMS)
	if err != nil || fresh || id2 != id {
		t.Fatalf("second claim: %v %v %v", id2, fresh, err)
	}
	if ok, err := r.dispatcher.FinishAttempt(ctx, id, "unknown", ""); err != nil || !ok {
		t.Fatalf("settle: %v %v", ok, err)
	}
	if ok, _ := r.dispatcher.FinishAttempt(ctx, id, "sent", "ATXid_1"); ok {
		t.Fatal("a settled attempt was settled again")
	}
}

func gazetteerVersion(t *testing.T, c *pgtest.TestCluster) string {
	t.Helper()
	f, _, err := loadGaz(t, c, gazYAML)
	if err != nil {
		t.Fatal(err)
	}
	return f.Version
}
