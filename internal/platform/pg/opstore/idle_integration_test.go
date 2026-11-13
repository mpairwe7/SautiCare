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
