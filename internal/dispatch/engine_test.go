package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/responder"
)

// claimOnly is a Store that only claims: every claim returns full batches
// while full > 0, then nothing.
type claimOnly struct {
	Store
	claims atomic.Int32
	full   atomic.Int32
}

func (c *claimOnly) DispatchSuspended(context.Context) (bool, error) { return true, nil } // the rule check is skipped

func (c *claimOnly) ClaimDueDispatch(_ context.Context, limit int, _ time.Duration) ([]Due, error) {
	c.claims.Add(1)
	if c.full.Add(-1) >= 0 {
		return make([]Due, 0, limit)[:0], nil
	}
	return nil, nil
}

// The worker polls on its interval and stops with its context; the poll
// loop is driven by a fake clock (testing/synctest).
func TestRunPollsOnItsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &claimOnly{}
		e := &Engine{store: store, suspension: store}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- e.Run(ctx, time.Second, 10, time.Minute, func(uuid.UUID, error) {}) }()
		time.Sleep(5*time.Second + 500*time.Millisecond)
		synctest.Wait()
		if n := store.claims.Load(); n != 6 {
			t.Fatalf("claims after 5.5 s = %d, want 6 (t = 0..5 s)", n)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	})
}

// ruleStore answers the stopping-rule reads and records an auto-suspension.
type ruleStore struct {
	Store
	suspended bool
	outcomes  []bool
	suspends  [][2]int
}

func (s *ruleStore) DispatchSuspended(context.Context) (bool, error) { return s.suspended, nil }
func (s *ruleStore) RedDispatchOutcomes(_ context.Context, n int, _ time.Duration) ([]bool, error) {
	return s.outcomes[:min(n, len(s.outcomes))], nil
}
func (s *ruleStore) AutoSuspendDispatch(_ context.Context, failures, window int) (bool, error) {
	s.suspends = append(s.suspends, [2]int{failures, window})
	s.suspended = true
	return true, nil
}

// The leader checks the stopping rule after each pass: 5 of 30 red
// failures suspends dispatch with the numbers registered; 4 of 30 does not;
// an already suspended dispatch is left alone.
func TestCheckStoppingRule(t *testing.T) {
	rule := StoppingRule{Window: 30, MaxFailureRate: 0.15}
	mk := func(failures int) []bool {
		out := make([]bool, 30)
		for i := range out {
			out[i] = i >= failures
		}
		return out
	}
	for _, tc := range []struct {
		failures  int
		suspended bool
		want      [][2]int
	}{{4, false, nil}, {5, false, [][2]int{{5, 30}}}, {9, true, nil}} {
		s := &ruleStore{outcomes: mk(tc.failures), suspended: tc.suspended}
		e := &Engine{store: s, suspension: s, rule: rule}
		breached, err := e.CheckStoppingRule(context.Background())
		if err != nil || breached != (tc.want != nil) || len(s.suspends) != len(tc.want) || (tc.want != nil && s.suspends[0] != tc.want[0]) {
			t.Errorf("%d failures (suspended %v): breached=%v suspends=%v err=%v", tc.failures, tc.suspended, breached, s.suspends, err)
		}
	}
}

// Review I3: intake records a ward-level location with the ward's centre as
// its zone, so the engine must look at the location quality: a ward-level
// incident searches from every zone of the ward, a landmark from its zone.
func TestCentresForAWardLevelLocation(t *testing.T) {
	ward := location.Ward{ID: "w9", Centre: location.Cell{Col: 10, Row: 10},
		Zones: []location.Cell{{Col: 10, Row: 10}, {Col: 13, Row: 10}}}
	e := &Engine{gazetteer: location.File{Wards: []location.Ward{ward}}}
	zone := &incident.Zone{Col: 10, Row: 10}
	got, cell := e.centres(Snapshot{Zone: zone, Ward: "w9", LocationQuality: incident.QualityWard})
	if len(got) != 2 || cell != ward.Centre {
		t.Fatalf("ward-level centres %v (%v), want every zone of the ward", got, cell)
	}
	far := location.Cell{Col: 14, Row: 10} // 4 cells from the centre, 1 from the ward's other zone
	if d := min(got[0].Chebyshev(far), got[1].Chebyshev(far)); d != 1 {
		t.Fatalf("distance %d", d)
	}
	if got, _ := e.centres(Snapshot{Zone: zone, Ward: "w9", LocationQuality: incident.QualityLandmark}); len(got) != 1 || got[0] != (location.Cell{Col: 10, Row: 10}) {
		t.Fatalf("landmark centres %v", got)
	}
}

// hangStore claims one incident whose snapshot never comes back until the
// pass is cancelled (a provider or a lock that hangs), and records the
// escalation that follows.
type hangStore struct {
	Store
	claimed   bool
	escalated []string
}

func (h *hangStore) DispatchSuspended(context.Context) (bool, error) { return true, nil }
func (h *hangStore) ClaimDueDispatch(context.Context, int, time.Duration) ([]Due, error) {
	if h.claimed {
		return nil, nil
	}
	h.claimed = true
	return []Due{{IncidentID: uuid.NewV7(), Gen: 1}}, nil
}
func (h *hangStore) DispatchSnapshot(ctx context.Context, _ uuid.UUID) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}
func (h *hangStore) DispatchEscalate(_ context.Context, _ uuid.UUID, reason string, _ responder.Role, _ string) (bool, error) {
	h.escalated = append(h.escalated, reason)
	return true, nil
}

// Review I8: every pass has a deadline below the lease, so a hang cannot
// stall the leader; the hung incident is escalated, and only a successful
// pass counts as the worker being alive.
func TestPassDeadlineAndLastPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := &hangStore{}
		e := &Engine{store: h, suspension: h, passTimeout: 20 * time.Second}
		start := time.Now()
		var failed int
		if _, err := e.RunOnce(t.Context(), 10, time.Minute, func(uuid.UUID, error) { failed++ }); err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took != 20*time.Second || failed != 1 || len(h.escalated) != 1 || h.escalated[0] != ReasonDispatchError {
			t.Fatalf("pass took %v, failures %d, escalations %v", took, failed, h.escalated)
		}
		if !e.LastPass().IsZero() {
			t.Fatal("a failed pass counted as alive")
		}
		if _, err := e.RunOnce(t.Context(), 10, time.Minute, func(uuid.UUID, error) {}); err != nil {
			t.Fatal(err)
		}
		if !e.LastPass().Equal(time.Now()) {
			t.Fatalf("last pass %v, want now", e.LastPass())
		}
	})
}
