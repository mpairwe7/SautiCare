package pg

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errConnClosed = errors.New("conn closed")

// fakeSession behaves like the pgx connection that holds the lock: a ping
// interrupted by its context closes the connection (pgx closes a connection
// whose read is cut short), and a closed connection cannot unlock.
type fakeSession struct {
	pingDelay time.Duration // how long a ping takes to answer
	pingErr   error         // if set, every ping fails with it (a dead session)
	pinging   chan struct{} // closed when the first ping starts
	once      sync.Once

	closed, discarded, released atomic.Bool
}

func newFakeSession(delay time.Duration, pingErr error) *fakeSession {
	return &fakeSession{pingDelay: delay, pingErr: pingErr, pinging: make(chan struct{})}
}

func (f *fakeSession) Ping(ctx context.Context) error {
	f.once.Do(func() { close(f.pinging) })
	if f.closed.Load() {
		return errConnClosed
	}
	if f.pingErr != nil {
		return f.pingErr
	}
	select {
	case <-ctx.Done():
		f.closed.Store(true)
		return ctx.Err()
	case <-time.After(f.pingDelay):
		return nil
	}
}

func (f *fakeSession) Unlock(context.Context, int64) error {
	if f.closed.Load() {
		return errConnClosed
	}
	return nil
}

func (f *fakeSession) Discard(context.Context) {
	f.closed.Store(true)
	f.discarded.Store(true)
}

func (f *fakeSession) Release() { f.released.Store(true) }

// runLead runs lead and fails the test if it does not return in time.
func runLead(ctx context.Context, t *testing.T, s lockSession, retry time.Duration, fn func(context.Context) error) (bool, error) {
	t.Helper()
	type result struct {
		lost bool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		lost, err := lead(ctx, s, 1, retry, fn)
		done <- result{lost, err}
	}()
	select {
	case r := <-done:
		return r.lost, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("lead did not return")
		return false, nil
	}
}

// CodeRabbit PR #3 (review of ead62cf), finding 7: fn returning while a ping
// is in flight is not lost leadership. Cancelling fn's context used to
// cancel the ping too; pgx then closed the lock connection, the unlock
// failed, and RunAsLeader discarded fn's error (a poison row that needs a
// human) and ran fn again.
func TestLeadKeepsFnResultWhenFnReturnsDuringAPing(t *testing.T) {
	fatal := errors.New("poison row: needs a human")
	s := newFakeSession(20*time.Millisecond, nil)
	lost, err := runLead(context.Background(), t, s, 200*time.Millisecond, func(context.Context) error {
		<-s.pinging // return while the first ping waits for its answer
		return fatal
	})
	if lost || !errors.Is(err, fatal) {
		t.Fatalf("lead = (lost %v, %v), want (false, %v)", lost, err, fatal)
	}
	if s.discarded.Load() || !s.released.Load() {
		t.Fatalf("lock connection discarded = %v, released = %v; want it unlocked and released",
			s.discarded.Load(), s.released.Load())
	}
}

// A failed ping while fn runs is still lost leadership: fn is stopped, and
// the dead connection is closed, never recycled.
func TestLeadReportsLossWhenAPingFails(t *testing.T) {
	s := newFakeSession(0, errors.New("connection reset"))
	lost, err := runLead(context.Background(), t, s, 10*time.Millisecond, func(lctx context.Context) error {
		<-lctx.Done()
		return lctx.Err()
	})
	if !lost || !errors.Is(err, context.Canceled) {
		t.Fatalf("lead = (lost %v, %v), want (true, context.Canceled)", lost, err)
	}
	if !s.discarded.Load() {
		t.Fatal("the dead lock connection was not discarded")
	}
}

// Shutdown is never held up by a ping: the ping ends with the process's
// context.
func TestLeadReturnsPromptlyOnShutdownDuringAPing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newFakeSession(time.Hour, nil)
	go func() {
		<-s.pinging
		cancel()
	}()
	start := time.Now()
	_, err := runLead(ctx, t, s, 10*time.Millisecond, func(lctx context.Context) error {
		<-lctx.Done()
		return lctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lead err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("shutdown waited %v for a ping", d)
	}
}
