package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"
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
func (s *ruleStore) RedDispatchOutcomes(_ context.Context, n int) ([]bool, error) {
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
