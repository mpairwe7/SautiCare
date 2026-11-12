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
		e := &Engine{store: store}
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
