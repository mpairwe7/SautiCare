//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/responder"
)

// Surge (Build Prompt §10.4): more emergencies than responders — incidents
// queue by severity, then by T2 (the oldest call first), and nothing is
// dropped; the surge flag (waiting incidents against free responders) feeds
// the console (M5) and sautictl.
func TestSurgeQueuesBySeverityThenT2(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	owner := r.c.Owner(t, pg.StoreOperational)
	older := dispatchedAs(t, r.intake, incident.SevYellow).IncidentID
	newer := dispatchedAs(t, r.intake, incident.SevYellow).IncidentID
	// The newer call's wake comes first; the older call still goes first.
	if _, err := owner.Exec(ctx, `UPDATE op.dispatch_timers SET due_at = clock_timestamp() - interval '1 minute', lease_until = NULL
		WHERE incident_id = $1`, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE op.dispatch_timers SET due_at = clock_timestamp() - interval '10 seconds', lease_until = NULL
		WHERE incident_id = $1`, older); err != nil {
		t.Fatal(err)
	}
	due, err := r.dispatcher.ClaimDueDispatch(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, d := range due {
		pos[d.IncidentID.String()] = i
	}
	// Every call is claimed (CodeRabbit PR #12: a missing one would read as
	// position 0 and pass the order check).
	red, okRed := pos[r.incident.String()]
	o, okOlder := pos[older.String()]
	n, okNewer := pos[newer.String()]
	if !okRed || !okOlder || !okNewer || red != 0 || o > n {
		t.Fatalf("claim order %v: want the red call first, then the yellow calls by T2", due)
	}

	s, err := r.ro.DispatchSurge(ctx)
	if err != nil || s.Waiting != 3 || s.Available != 0 || !s.Surge {
		t.Fatalf("three calls, nobody free: %+v %v", s, err)
	}
	for range 3 {
		r.responder(t, responder.RoleVHT)
	}
	if s, _ := r.ro.DispatchSurge(ctx); s.Available != 3 || s.Surge {
		t.Fatalf("three free responders: %+v", s)
	}
}
