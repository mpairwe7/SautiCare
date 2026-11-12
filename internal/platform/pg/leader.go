package pg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunAsLeader blocks until this process holds the session advisory lock key
// on a dedicated connection, then runs fn. fn's context is cancelled if the
// lock connection stops answering; PostgreSQL releases a session lock when
// its connection dies, so a standby then takes over. It returns when ctx
// ends or fn returns. Used to keep exactly one outbox-relay active, which
// preserves ledger order without a cluster coordinator.
func RunAsLeader(ctx context.Context, pool *pgxpool.Pool, key int64, retry time.Duration, fn func(context.Context) error) error {
	for {
		conn, err := pool.Acquire(ctx)
		if err == nil {
			var got bool
			if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err == nil && got {
				lctx, cancel := context.WithCancel(ctx)
				go func() {
					t := time.NewTicker(retry)
					defer t.Stop()
					for {
						select {
						case <-lctx.Done():
							return
						case <-t.C:
							pctx, pcancel := context.WithTimeout(lctx, retry)
							if conn.Ping(pctx) != nil {
								pcancel()
								cancel()
								return
							}
							pcancel()
						}
					}
				}()
				ferr := fn(lctx)
				cancel()
				_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key)
				conn.Release()
				return ferr
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
