package pg

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunAsLeader keeps this process campaigning for the session advisory lock
// key and runs fn whenever it holds it. Exactly one process holds the lock,
// which keeps one outbox-relay active and the ledger in order without a
// cluster coordinator (ADR-0004).
//
// If the lock connection stops answering — Patroni switchover, a restart, a
// network blip, a terminated backend — PostgreSQL releases the lock; fn's
// context is cancelled and RunAsLeader campaigns again (review finding I1).
// It returns only when ctx ends, or when fn returns on its own while still
// leader (e.g. a poison row that needs a human).
//
// Every call on the lock connection has a deadline (leaderOpTimeout): pool
// acquisition, the lock attempt, each ping (at most retry), and the unlock
// and close on the way out, which run on a fresh bounded context so shutdown
// cannot skip or hang them. fn finishing never interrupts a ping, so it is
// never mistaken for lost leadership and fn's result is kept.
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, key int64, retry time.Duration, fn func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if conn := campaign(ctx, pool, key); conn != nil {
			lost, ferr := lead(ctx, pooledSession{conn}, key, retry, fn)
			if !lost || ctx.Err() != nil {
				return ferr
			}
			continue // leadership lost while the process lives: campaign again
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
	}
}

// leaderOpTimeout bounds each call on the lock connection.
const leaderOpTimeout = 5 * time.Second

// cleanupContext is a bounded context for releasing the lock after ctx may
// have ended.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), leaderOpTimeout)
}

// campaign tries once to take the lock and returns the connection holding
// it, or nil.
func campaign(ctx context.Context, pool *pgxpool.Pool, key int64) *pgxpool.Conn {
	actx, cancel := context.WithTimeout(ctx, leaderOpTimeout)
	defer cancel()
	conn, err := pool.Acquire(actx)
	if err != nil {
		return nil
	}
	var got bool
	if err := conn.QueryRow(actx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		// The outcome is unknown (the lock may have been granted before a
		// timeout): close the session, which releases any lock it holds,
		// rather than return it to the pool.
		cctx, ccancel := cleanupContext(ctx)
		_ = conn.Conn().Close(cctx)
		ccancel()
		conn.Release()
		return nil
	}
	if !got {
		conn.Release()
		return nil
	}
	return conn
}

// lockSession is the connection holding the lock (a seam for unit tests).
type lockSession interface {
	Ping(ctx context.Context) error
	Unlock(ctx context.Context, key int64) error
	// Discard closes the connection, which releases the session lock
	// server-side if it is still held, and gives it back to the pool.
	Discard(ctx context.Context)
	// Release gives the healthy, unlocked connection back to the pool.
	Release()
}

type pooledSession struct{ c *pgxpool.Conn }

func (s pooledSession) Ping(ctx context.Context) error { return s.c.Ping(ctx) }

func (s pooledSession) Unlock(ctx context.Context, key int64) error {
	_, err := s.c.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
	return err
}

func (s pooledSession) Discard(ctx context.Context) {
	_ = s.c.Conn().Close(ctx)
	s.c.Release()
}

func (s pooledSession) Release() { s.c.Release() }

// lead runs fn while holding the lock on conn. It reports whether leadership
// was lost — the lock connection failed, either under the pinger or at the
// unlock — rather than fn finishing.
func lead(ctx context.Context, conn lockSession, key int64, retry time.Duration, fn func(context.Context) error) (bool, error) {
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lost atomic.Bool
	stop := make(chan struct{}) // closed when fn returns
	pinger := make(chan struct{})
	go func() {
		defer close(pinger)
		t := time.NewTicker(retry)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			select {
			case <-stop: // fn finished as the ticker fired: no new ping
				return
			default:
			}
			// The ping hangs off ctx, not lctx, and is bounded on its own. fn
			// returning cancels lctx; were the ping tied to it, pgx would close
			// the interrupted connection, the unlock would fail, and fn's own
			// result would be discarded as lost leadership (CodeRabbit PR #3).
			pctx, pcancel := context.WithTimeout(ctx, min(retry, leaderOpTimeout))
			err := conn.Ping(pctx)
			pcancel()
			if err != nil && ctx.Err() == nil {
				lost.Store(true)
				cancel() // stop fn: leadership is gone
				return
			}
		}
	}()

	ferr := fn(lctx)
	// Stop the pinger and join it before touching conn again (review finding
	// M11): concurrent use could skip the unlock and return a lock-holding
	// connection to the pool. A ping already in flight is bounded and is
	// allowed to finish.
	close(stop)
	<-pinger
	cancel()

	cctx, ccancel := cleanupContext(ctx)
	defer ccancel()
	if lost.Load() {
		conn.Discard(cctx) // already dead; never recycle it
		return true, ferr
	}
	if err := conn.Unlock(cctx, key); err != nil {
		// The lock connection failed. Closing the session releases the
		// session-level lock server-side if it is still held; either way this
		// process is no longer leader and campaigns again (review finding,
		// CodeRabbit PR #3).
		conn.Discard(cctx)
		return true, ferr
	}
	conn.Release()
	return false, ferr
}
