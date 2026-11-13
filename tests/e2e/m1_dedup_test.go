//go:build integration

package e2e

import (
	"context"
	"testing"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

func (r *intakeRig) projections(t *testing.T) []incident.Projection {
	t.Helper()
	ids, err := r.ro.ListIncidentIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]incident.Projection, 0, len(ids))
	for _, id := range ids {
		p, err := r.ro.Projection(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func byState(ps []incident.Projection, s incident.State) []incident.Projection {
	var out []incident.Projection
	for _, p := range ps {
		if p.State == s {
			out = append(out, p)
		}
	}
	return out
}

// Scenario 4 (Build Prompt §16): three bystanders call about one crash within
// 15 minutes. The calls become one incident whose T1 is the earliest call,
// no call is discarded, and the coordinator splits a wrong merge, logged.
func TestScenario4ThreeBystandersOneIncident(t *testing.T) {
	r := newIntakeRig(t)
	ctx := context.Background()
	a := r.ussd.Dial("+256772000001") // first to dial, last to finish
	walk(t, a, "", "1", "1", "1", "3", "2", "1")
	b := r.ussd.Dial("+256772000002")
	walk(t, b, "", "1", "1", "1", "3", "2", "1", "1", "1") // reaches dispatch first
	c := r.ussd.Dial("+256772000003")
	walk(t, c, "", "1", "1", "1", "3", "2", "1", "2", "1") // a neighbouring landmark
	walk(t, a, "3", "1")

	all := r.projections(t)
	survivors, merged := byState(all, incident.StateDispatching), byState(all, incident.StateMerged)
	if len(all) != 3 || len(survivors) != 1 || len(merged) != 2 {
		t.Fatalf("incidents %d, dispatching %d, merged %d; want 3 / 1 / 2", len(all), len(survivors), len(merged))
	}
	s := survivors[0]
	earliest := s.OwnT1
	for _, m := range merged {
		if m.MergedInto == nil || *m.MergedInto != s.ID || m.LastScreen != 8 {
			t.Fatalf("member %+v", m)
		}
		if m.OwnT1.Before(earliest) {
			earliest = m.OwnT1
		}
	}
	if !s.T[1].Equal(earliest) || s.T[1].Equal(s.OwnT1) || len(s.Absorbed) != 2 {
		t.Fatalf("survivor T1 %v own %v absorbed %v, want the earliest call %v", s.T[1], s.OwnT1, s.Absorbed, earliest)
	}

	// The coordinator splits the latest member off.
	pool, err := pg.Connect(ctx, r.c.DSN(pg.StoreOperational, "op_console"), "e2e-console", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	member := merged[0]
	if merged[1].OwnT1.After(member.OwnT1) {
		member = merged[1]
	}
	staff := uuid.NewV7()
	if err := opstore.New(pool).SplitIncident(ctx, s.ID, member.ID, staff); err != nil {
		t.Fatal(err)
	}
	after := r.projections(t)
	if n := len(byState(after, incident.StateDispatching)); n != 2 {
		t.Fatalf("after the split: %d dispatching, want 2", n)
	}
	sp, _ := r.ro.Projection(ctx, s.ID)
	if len(sp.Absorbed) != 1 || !sp.T[1].Equal(earliest) {
		t.Fatalf("survivor after split: %+v", sp)
	}
	r.noDrift(t)
}
