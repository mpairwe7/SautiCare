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
// acquisition, the lock attempt, and the unlock and close on the way out,
// which run on a fresh bounded context so shutdown cannot skip or hang them.
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, key int64, retry time.Duration, fn func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if conn := campaign(ctx, pool, key); conn != nil {
			lost, ferr := lead(ctx, conn, key, retry, fn)
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

// lead runs fn while holding the lock on conn. It reports whether leadership
// was lost — the lock connection failed, either under the pinger or at the
// unlock — rather than fn finishing.
func lead(ctx context.Context, conn *pgxpool.Conn, key int64, retry time.Duration, fn func(context.Context) error) (bool, error) {
	lctx, cancel := context.WithCancel(ctx)
	var lost atomic.Bool
	pinger := make(chan struct{})
	go func() {
		defer close(pinger)
		t := time.NewTicker(retry)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(lctx, retry)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil && lctx.Err() == nil {
					lost.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	ferr := fn(lctx)
	cancel()
	// Join the pinger before touching conn again (review finding M11):
	// concurrent use could skip the unlock and return a lock-holding
	// connection to the pool.
	<-pinger

	cctx, ccancel := cleanupContext(ctx)
	defer ccancel()
	if lost.Load() {
		_ = conn.Conn().Close(cctx) // already dead; never recycle it
		conn.Release()
		return true, ferr
	}
	if _, err := conn.Exec(cctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
		// The lock connection failed. Closing the session releases the
		// session-level lock server-side if it is still held; either way this
		// process is no longer leader and campaigns again (review finding,
		// CodeRabbit PR #3).
		_ = conn.Conn().Close(cctx)
		conn.Release()
		return true, ferr
	}
	conn.Release()
	return false, ferr
}
