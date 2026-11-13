package pg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is what a store adapter runs statements and transactions on;
// *pgxpool.Pool implements it.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// BoundedDB gives every call on a DB its own deadline (CodeRabbit PR #3).
//
// PostgreSQL's statement_timeout (Connect) stops a statement once it runs,
// but nothing bounds the wait for a pool connection, so a caller without a
// deadline could wait for ever on an exhausted pool. Each call — the pool
// wait and one statement — gets at most the bound; a caller's earlier
// deadline, or its cancellation, still applies. A query keeps its context
// until its rows are closed, and a row until it is scanned.
//
// A transaction is bounded only while it begins: whoever holds it ends it,
// and the server's idle_in_transaction_session_timeout ends a forgotten one.
type BoundedDB struct {
	db      DB
	timeout time.Duration
}

// Bounded wraps db so that every call on it has a deadline of at most timeout.
func Bounded(db DB, timeout time.Duration) *BoundedDB {
	return &BoundedDB{db: db, timeout: timeout}
}

// WithTimeout returns the same database with another per-call bound.
func (b *BoundedDB) WithTimeout(timeout time.Duration) *BoundedDB {
	return &BoundedDB{db: b.db, timeout: timeout}
}

// Exec runs one statement under the bound.
func (b *BoundedDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.db.Exec(ctx, sql, args...)
}

// Query runs one query under the bound, which lasts until the rows close.
func (b *BoundedDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	rows, err := b.db.Query(ctx, sql, args...)
	if err != nil {
		cancel()
		return rows, err
	}
	return &boundedRows{Rows: rows, cancel: cancel}, nil
}

// QueryRow runs one query under the bound, which lasts until the row is scanned.
func (b *BoundedDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	return &boundedRow{row: b.db.QueryRow(ctx, sql, args...), cancel: cancel}
}

// Begin starts a transaction; only the start is bounded.
func (b *BoundedDB) Begin(ctx context.Context) (pgx.Tx, error) {
	return b.BeginTx(ctx, pgx.TxOptions{})
}

// BeginTx starts a transaction; only the start (pool wait and BEGIN) is
// bounded. pgx uses the context for nothing else.
func (b *BoundedDB) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.db.BeginTx(ctx, opts)
}

type boundedRows struct {
	pgx.Rows
	cancel context.CancelFunc
}

func (r *boundedRows) Close() {
	r.Rows.Close()
	r.cancel()
}

type boundedRow struct {
	row    pgx.Row
	cancel context.CancelFunc
}

func (r *boundedRow) Scan(dest ...any) error {
	defer r.cancel()
	return r.row.Scan(dest...)
}
