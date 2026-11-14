//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// Only open, unfinished incidents idle for the given time and not yet
// flagged abandoned are listed, with what the sweeper needs.
func TestListIdle(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	open := func() incident.Opened {
		o, err := s.OpenIncident(ctx, openParams(t))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	idle, fresh, flagged, done, undispatched := open(), open(), open(), open(), open()
	appendEv(t, s, idle.IncidentID, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"yes"}`, "")
	appendEv(t, s, idle.IncidentID, incident.EvClassified, `{"severity":"red"}`, testLogic)
	appendEv(t, s, flagged.IncidentID, incident.EvFlagged, `{"flag":"abandoned"}`, "")
	for _, o := range []incident.Opened{done, undispatched} {
		appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":8,"field":"callback","value":"same_number"}`, "")
	}
	owner := c.Owner(t, pg.StoreOperational)
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET t2 = clock_timestamp() WHERE id = $1", done.IncidentID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET updated_at = clock_timestamp() - interval '5 minutes' WHERE id <> $1",
		fresh.IncidentID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIdle(ctx, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A finished Tool 1 is idle only if T2 was never recorded (C1): it is
	// still the coordinator's to follow up.
	ids := map[uuid.UUID]intake.Idle{}
	for _, in := range got {
		ids[in.ID] = in
	}
	if len(got) != 2 || ids[idle.IncidentID].Danger != "yes" || ids[idle.IncidentID].Category != "" || ids[idle.IncidentID].Dispatched ||
		ids[idle.IncidentID].Severity != incident.SevRed {
		t.Fatalf("idle = %+v", got)
	}
	if u, ok := ids[undispatched.IncidentID]; !ok || u.Dispatched {
		t.Fatalf("finished without T2 not listed: %+v", got)
	}
}

// CodeRabbit PR #6: the abandoned flag is never cleared, but a caller can
// resume an abandoned incident, answer danger yes (or be classified red) and
// drop again before T2. That incident is listed again while the escalation
// its abandonment owes is missing, and only then: escalated, dispatched, or
// owing none, it stays off the list.
func TestListIdleRelistsAnAbandonmentThatOwesItsEscalation(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	open := func() uuid.UUID {
		o, err := s.OpenIncident(ctx, openParams(t))
		if err != nil {
			t.Fatal(err)
		}
		appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"abandoned"}`, "")
		appendEv(t, s, o.IncidentID, incident.EvSessionResumed, `{"channel":"ussd_at"}`, "")
		return o.IncidentID
	}
	dangerYes, red, escalated, dispatched, owesNone := open(), open(), open(), open(), open()
	for _, id := range []uuid.UUID{dangerYes, escalated, dispatched} {
		appendEv(t, s, id, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"yes"}`, "")
	}
	appendEv(t, s, red, incident.EvClassified, `{"severity":"red"}`, testLogic)
	appendEv(t, s, escalated, incident.EvEscalated, `{"reason":"abandoned_after_danger_yes"}`, "")
	appendEv(t, s, owesNone, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"no"}`, "")
	owner := c.Owner(t, pg.StoreOperational)
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET t2 = clock_timestamp() WHERE id = $1", dispatched); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET updated_at = clock_timestamp() - interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIdle(ctx, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[uuid.UUID]bool{}
	for _, in := range got {
		listed[in.ID] = true
	}
	if len(got) != 2 || !listed[dangerYes] || !listed[red] {
		t.Fatalf("listed %v, want the danger-yes and the red abandonment only", got)
	}
}

// F-04: the sweeper learns the ward an idle call gave, so a located call
// whose dispatch never started reaches a coordinator.
func TestListIdleCarriesTheWard(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":6,"field":"ward","value":"w01"}`, "")
	if _, err := c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.incidents SET updated_at = clock_timestamp() - interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIdle(ctx, 2*time.Minute)
	if err != nil || len(got) != 1 || got[0].Ward != "w01" {
		t.Fatalf("idle %+v %v", got, err)
	}
}

// F-05: a dispatching incident whose dispatch timer is overdue by the
// watchdog time, unleased, is a stall even after T3 (the worker died after
// the first alert). intake-gw's watchdog (op_intake) escalates it once; a
// leased timer is the worker at work and is left alone.
func TestWatchdogEscalatesAnOverdueTimerAfterT3(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	owner := r.c.Owner(t, pg.StoreOperational)
	leased := dispatchedIncident(t, r.intake).IncidentID
	for _, q := range []string{
		"UPDATE op.incidents SET t3 = clock_timestamp() WHERE id IN ($1, $2)",
		`INSERT INTO op.dispatch_timers (incident_id, due_at, lease_until)
		 VALUES ($1, clock_timestamp() - interval '3 minutes', NULL),
		        ($2, clock_timestamp() - interval '3 minutes', clock_timestamp() + interval '1 minute')
		 ON CONFLICT (incident_id) DO UPDATE SET due_at = excluded.due_at, lease_until = excluded.lease_until`,
	} {
		if _, err := owner.Exec(ctx, q, r.incident, leased); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	got, err := r.intake.ListOverdueDispatch(ctx, intake.DispatchWatchdogAfter)
	if err != nil || len(got) != 1 || got[0].ID != r.incident || got[0].DueAt.IsZero() {
		t.Fatalf("overdue %+v %v", got, err)
	}
	w := intake.NewWatchdog(r.intake, r.intake, intake.DispatchWatchdogAfter)
	for range 2 {
		if _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM op.incident_events WHERE incident_id = $1 AND type = 'incident.escalated'
		AND payload ->> 'reason' = 'dispatch_stalled'`, r.incident).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dispatch_stalled escalations %d %v, want 1", n, err)
	}
}

// Review R2: an abandoned call is listed again exactly when the sweeper
// would escalate it (intake.AbandonmentEscalation), so the SQL and the Go
// rule cannot drift apart: a resumed yellow call that gave its ward, then
// dropped again before T2, is relisted and owed dispatch_not_started.
func TestListIdleRelistsWhatTheSweeperWouldEscalate(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	want := map[uuid.UUID]bool{}
	for _, danger := range []string{"no", "yes"} {
		for _, sev := range []string{"yellow", "red"} {
			for _, ward := range []string{"", "w01", "elsewhere"} {
				o, err := s.OpenIncident(ctx, openParams(t))
				if err != nil {
					t.Fatal(err)
				}
				appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":2,"field":"danger","value":"`+danger+`"}`, "")
				appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"abandoned"}`, "")
				appendEv(t, s, o.IncidentID, incident.EvSessionResumed, `{"channel":"ussd_at"}`, "")
				appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"`+sev+`"}`, testLogic)
				if ward != "" {
					appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":6,"field":"ward","value":"`+ward+`"}`, "")
				}
				want[o.IncidentID] = intake.AbandonmentEscalation(intake.Idle{Danger: danger, Severity: incident.Severity(sev), Ward: ward}) != ""
			}
		}
	}
	if _, err := c.Owner(t, pg.StoreOperational).Exec(ctx, "UPDATE op.incidents SET updated_at = clock_timestamp() - interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIdle(ctx, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[uuid.UUID]bool{}
	for _, in := range got {
		listed[in.ID] = true
	}
	for id, owes := range want {
		if listed[id] != owes {
			t.Errorf("incident %s: listed %v, the sweeper would escalate %v", id, listed[id], owes)
		}
	}
}
