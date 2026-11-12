//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
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

// Red dispatch outcomes for the stopping rule, newest T2 first (review I2):
// a success is an acceptance within the red window (3 min, decision 2) of
// T2, whichever role; a failure is the window passing without one. An
// incident closed inside the window without acceptance, or not yet past it,
// is not judged; merged incidents are not judged; and only incidents
// dispatched after the latest resumption count, so a coordinator's resume is
// never undone by the evidence that caused the suspension.
func TestRedDispatchOutcomes(t *testing.T) {
	r := newDispatchRig(t) // r.incident: red, dispatched just now, unsettled
	ctx := context.Background()
	owner := r.c.Owner(t, pg.StoreOperational)
	const window = 3 * time.Minute
	red := func(t2Ago time.Duration, t4After *time.Duration) uuid.UUID {
		t.Helper()
		id := dispatchedIncident(t, r.intake).IncidentID
		if _, err := owner.Exec(ctx, `UPDATE op.incidents SET t2 = clock_timestamp() - $2::interval,
			t4 = CASE WHEN $3::interval IS NULL THEN NULL ELSE clock_timestamp() - $2::interval + $3::interval END WHERE id = $1`,
			id, t2Ago, t4After); err != nil {
			t.Fatal(err)
		}
		return id
	}
	d := func(x time.Duration) *time.Duration { return &x }
	red(20*time.Minute, d(time.Minute))                 // success
	red(19*time.Minute, nil)                            // failure: no acceptance in the window
	red(18*time.Minute, d(5*time.Minute))               // failure: accepted after the window
	crossRole := red(17*time.Minute, d(10*time.Second)) // success, though the other role had nobody
	if _, err := r.dispatcher.DispatchEscalate(ctx, crossRole, dispatch.ReasonExhausted, responder.RoleTransporter, dispatchLogic); err != nil {
		t.Fatal(err)
	}
	// Closed one minute after T2 without an acceptance, window long over: a
	// caller's or coordinator's decision, not judged.
	cancelled := red(10*time.Minute, nil)
	appendEv(t, r.intake, cancelled, incident.EvClosed, `{"reason":"caller_cancelled"}`, "")
	if _, err := owner.Exec(ctx, `UPDATE op.incident_events e SET recorded_at = i.t2 + interval '1 minute'
		FROM op.incidents i WHERE i.id = e.incident_id AND e.incident_id = $1 AND e.type = 'incident.closed'`, cancelled); err != nil {
		t.Fatal(err)
	}
	red(30*time.Second, nil) // window not over: not judged
	yellow := classifiedAs(t, r.intake, "trauma", incident.SevYellow, false)
	appendEv(t, r.intake, yellow.IncidentID, incident.EvDispatchReady, `{"zone":{"col":902,"row":84},"location_quality":"landmark"}`, "")

	got, err := r.dispatcher.RedDispatchOutcomes(ctx, 30, window)
	if err != nil {
		t.Fatal(err)
	}
	if want := []bool{true, false, false, true}; !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v (newest first)", got, want)
	}
	if got, _ := r.dispatcher.RedDispatchOutcomes(ctx, 2, window); !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("window 2: %v", got)
	}
	// After a suspension and a coordinator's resumption, only incidents
	// dispatched since count: the earlier failures cannot re-suspend.
	if _, err := r.console.SuspendDispatch(ctx, "coordinator_decision", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.console.ResumeDispatch(ctx, "reviewed", uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.dispatcher.RedDispatchOutcomes(ctx, 30, window); len(got) != 0 {
		t.Fatalf("outcomes after the resumption %v, want none", got)
	}
}
