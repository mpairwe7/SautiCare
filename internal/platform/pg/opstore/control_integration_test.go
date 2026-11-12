//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/responder"
)

// O6: a coordinator suspends and resumes dispatch with a staff identity; each
// change is in the dated control log and the audit outbox (no incident), and
// wakes every incident in dispatch so the change applies at once.
func TestSuspensionControl(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	staff := uuid.NewV7()
	for _, s := range []interface {
		DispatchSuspended(context.Context) (bool, error)
	}{r.dispatcher, r.intake, r.ivr} {
		if on, err := s.DispatchSuspended(ctx); err != nil || on {
			t.Fatalf("initially suspended=%v %v", on, err)
		}
	}
	gen := func() int64 {
		var g int64
		if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT gen FROM op.dispatch_timers WHERE incident_id = $1", r.incident).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	before := gen()
	if changed, err := r.console.SuspendDispatch(ctx, "coordinator_decision", staff); err != nil || !changed {
		t.Fatalf("suspend: %v %v", changed, err)
	}
	if changed, _ := r.console.SuspendDispatch(ctx, "coordinator_decision", staff); changed {
		t.Fatal("suspending twice changed something")
	}
	if on, _ := r.dispatcher.DispatchSuspended(ctx); !on || gen() <= before {
		t.Fatalf("suspended=%v, timers woken=%v", on, gen() > before)
	}
	if _, err := r.console.ResumeDispatch(ctx, "coordinator_decision", uuid.UUID{}); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("resume without a staff identity: %v", err)
	}
	if changed, err := r.console.ResumeDispatch(ctx, "reviewed", staff); err != nil || !changed {
		t.Fatalf("resume: %v %v", changed, err)
	}
	log, err := r.ro.ControlLog(ctx, 10)
	if err != nil || len(log) != 2 || log[0].Action != "resumed" || log[1].Action != "suspended" || log[0].StaffID == nil || *log[0].StaffID != staff {
		t.Fatalf("control log %+v %v", log, err)
	}
	var audits int
	if err := r.c.Owner(t, pg.StoreOperational).QueryRow(ctx, `SELECT count(*) FROM op.outbox WHERE topic = 'audit'
		AND payload ->> 'event_type' IN ('dispatch.suspended', 'dispatch.resumed') AND payload -> 'incident_id' = 'null'::jsonb
		AND payload ->> 'staff_id' = $1`, staff.String()).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit outbox rows = %d, %v", audits, err)
	}
	for role, q := range map[string]string{
		"op_dispatch": "SELECT op.suspend_dispatch('x', gen_random_uuid())",
		"op_intake":   "SELECT op.resume_dispatch('x', gen_random_uuid())",
		"op_console":  "UPDATE op.dispatch_control SET suspended = false",
		"op_readonly": "DELETE FROM op.dispatch_control_log",
	} {
		conn, err := pg.Connect(ctx, r.c.DSN(pg.StoreOperational, role), "test", 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("%s: %s: %v, want permission denied", role, q, err)
		}
		conn.Close()
	}
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, "DELETE FROM op.dispatch_control_log"); pgCode(err) != "SA403" {
		t.Errorf("the control log is not append-only: %v", err)
	}
}

// The stopping rule may suspend automatically (op_dispatch); the breach is
// registered with its numbers, once.
func TestAutoSuspendRegistersTheBreach(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	if changed, err := r.dispatcher.AutoSuspendDispatch(ctx, 5, 30); err != nil || !changed {
		t.Fatalf("auto-suspend: %v %v", changed, err)
	}
	if changed, _ := r.dispatcher.AutoSuspendDispatch(ctx, 6, 30); changed {
		t.Fatal("a second breach while suspended changed something")
	}
	log, _ := r.ro.ControlLog(ctx, 10)
	if len(log) != 2 || log[1].Action != "stopping_rule_breached" || log[1].Failures != 5 || log[1].Window != 30 ||
		log[0].Action != "suspended" || log[0].Reason != "stopping_rule" || log[0].StaffID != nil {
		t.Fatalf("register %+v", log)
	}
}

// Red dispatch outcomes for the stopping rule (newest first): a failure is
// no acceptance before closure or a dispatch escalation for lack of one;
// unsettled, non-red, merged and suspended-time incidents do not count.
func TestRedDispatchOutcomes(t *testing.T) {
	r := newDispatchRig(t) // r.incident: red, dispatched, unsettled
	ctx := context.Background()
	accepted := func() uuid.UUID {
		id := dispatchedIncident(t, r.intake).IncidentID
		v := r.responder(t, responder.RoleVHT)
		a := r.issue(t, id, v, responder.RoleVHT)
		if res, err := r.intake.AcceptAlert(ctx, a.AlertID, v); err != nil || res.Result != dispatch.Accepted {
			t.Fatal(res, err)
		}
		return id
	}
	escalated := func(reason string) uuid.UUID {
		id := dispatchedIncident(t, r.intake).IncidentID
		if _, err := r.dispatcher.DispatchEscalate(ctx, id, reason, "", dispatchLogic); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ok1 := accepted()
	late := escalated(dispatch.ReasonRedNoAcceptance) // then accepted: still a failure
	v := r.responder(t, responder.RoleTransporter)
	a := r.issue(t, late, v, responder.RoleTransporter)
	if _, err := r.intake.AcceptAlert(ctx, a.AlertID, v); err != nil {
		t.Fatal(err)
	}
	closed := dispatchedIncident(t, r.intake).IncidentID
	appendEv(t, r.intake, closed, incident.EvClosed, `{"reason":"caller_cancelled"}`, "")
	// Handled by a coordinator during a suspension and closed without an
	// acceptance: not a dispatch failure (dispatch never ran), so excluded —
	// otherwise a suspension would feed the rule that caused it.
	suspendedTime := escalated(dispatch.ReasonSuspended)
	appendEv(t, r.intake, suspendedTime, incident.EvClosed, `{"reason":"handled_by_coordinator"}`, "")
	escalated("triage_rule_error") // not a dispatch failure: unsettled
	yellow := classifiedAs(t, r.intake, "trauma", incident.SevYellow, false)
	appendEv(t, r.intake, yellow.IncidentID, incident.EvDispatchReady, `{"zone":{"col":902,"row":84},"location_quality":"landmark"}`, "")
	ok2 := accepted()

	got, err := r.dispatcher.RedDispatchOutcomes(ctx, 30)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, true} // ok2, closed, late, ok1 (newest first)
	if !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	if got, _ := r.dispatcher.RedDispatchOutcomes(ctx, 2); !slices.Equal(got, want[:2]) {
		t.Fatalf("window 2: %v", got)
	}
	_, _ = ok1, ok2
}
