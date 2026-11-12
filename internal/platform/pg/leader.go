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
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, key int64, retry time.Duration, fn func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := pool.Acquire(ctx)
		if err == nil {
			var got bool
			if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err == nil && got {
				lost, ferr := lead(ctx, conn, key, retry, fn)
				if !lost || ctx.Err() != nil {
					return ferr
				}
				continue // leadership lost while the process lives: campaign again
			}
			conn.Release()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
	}
}

// lead runs fn while holding the lock on conn. It reports whether leadership
// was lost (the lock connection failed) rather than fn finishing.
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

	if lost.Load() {
		_ = conn.Conn().Close(context.WithoutCancel(ctx)) // already dead; never recycle it
		conn.Release()
		return true, ferr
	}
	if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key); err != nil {
		// Closing the session releases a session-level lock server-side.
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
	}
	conn.Release()
	return false, ferr
}
