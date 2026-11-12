//go:build integration

package opstore_test

import (
	"context"
	"testing"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// verify-projections must catch a projection that no longer equals the fold
// of its events (spec §6.2), e.g. after a manual edit or a restore mishap.
func TestVerifyProjectionsDetectsDrift(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	for range 3 {
		o, _ := s.OpenIncident(ctx, openParams(t))
		appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"yellow"}`, "tr-test")
	}
	ro := storeAs(t, c, "op_readonly")
	checked, drift, err := opstore.VerifyProjections(ctx, ro)
	if err != nil || checked != 3 || len(drift) != 0 {
		t.Fatalf("clean: checked=%d drift=%v err=%v", checked, drift, err)
	}
	if _, err := c.Owner(t, pg.StoreOperational).Exec(ctx,
		"UPDATE op.incidents SET severity = 'green' WHERE id = (SELECT id FROM op.incidents ORDER BY id LIMIT 1)"); err != nil {
		t.Fatal(err)
	}
	_, drift, err = opstore.VerifyProjections(ctx, ro)
	if err != nil || len(drift) != 1 {
		t.Fatalf("after tamper: drift=%v err=%v, want 1 drift", drift, err)
	}
}

// Review finding M3: on a live system an append can land between reading an
// incident's events and its projection. The verifier must read both from one
// snapshot, or the nightly job raises false drift alarms.
func TestVerifyProjectionsUsesConsistentSnapshot(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, _ := s.OpenIncident(ctx, openParams(t))
	raced := false
	restore := opstore.SetVerifyHook(func() {
		if !raced {
			raced = true
			appendEv(t, s, o.IncidentID, incident.EvScreenAnswered, `{"screen":2}`, "")
		}
	})
	defer restore()
	checked, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly"))
	if err != nil || checked != 1 {
		t.Fatalf("checked=%d err=%v", checked, err)
	}
	if !raced {
		t.Fatal("hook did not run")
	}
	if len(drift) != 0 {
		t.Fatalf("concurrent append reported as drift: %v", drift)
	}
}
