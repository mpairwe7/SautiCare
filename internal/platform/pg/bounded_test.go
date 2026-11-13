package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDB records the context each call receives.
type fakeDB struct{ ctxs []context.Context }

func (f *fakeDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	f.ctxs = append(f.ctxs, ctx)
	return pgconn.CommandTag{}, nil
}

func (f *fakeDB) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	f.ctxs = append(f.ctxs, ctx)
	return &fakeRows{ctx: ctx}, nil
}

func (f *fakeDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	f.ctxs = append(f.ctxs, ctx)
	return fakeRow{ctx: ctx}
}

func (f *fakeDB) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	f.ctxs = append(f.ctxs, ctx)
	return nil, nil
}

// fakeRows yields two rows, each only while its query's context is live.
type fakeRows struct {
	pgx.Rows
	ctx context.Context
	n   int
}

func (r *fakeRows) Next() bool {
	if r.ctx.Err() != nil || r.n == 2 {
		return false
	}
	r.n++
	return true
}

func (r *fakeRows) Err() error { return r.ctx.Err() }
func (r *fakeRows) Close()     {}

type fakeRow struct{ ctx context.Context }

func (r fakeRow) Scan(...any) error { return r.ctx.Err() }

func TestBoundedGivesEveryCallADeadline(t *testing.T) {
	f := &fakeDB{}
	b := Bounded(f, time.Minute)
	ctx := context.Background() // a caller without a deadline
	if _, err := b.Exec(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	rows, err := b.Query(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if err := b.QueryRow(ctx, "x").Scan(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.BeginTx(ctx, pgx.TxOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.ctxs) != 5 {
		t.Fatalf("recorded %d calls, want 5", len(f.ctxs))
	}
	for i, c := range f.ctxs {
		dl, ok := c.Deadline()
		if !ok || time.Until(dl) > time.Minute {
			t.Errorf("call %d: deadline %v (set %v), want one within a minute", i, dl, ok)
		}
	}
}

// The caller's own earlier deadline and its cancellation still apply.
func TestBoundedKeepsTheCallersDeadlineAndCancellation(t *testing.T) {
	f := &fakeDB{}
	b := Bounded(f, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.Exec(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	row := b.QueryRow(cctx, "x")
	ccancel()
	if err := row.Scan(); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan after the caller cancelled: %v, want context.Canceled", err)
	}
	if dl, _ := f.ctxs[0].Deadline(); time.Until(dl) > time.Second {
		t.Fatalf("deadline %v is later than the caller's", dl)
	}
}

// A query's rows are read under its context, so the bound must last until
// they are closed, not end when Query returns; then it is released.
func TestBoundedQueryKeepsItsContextUntilRowsClose(t *testing.T) {
	f := &fakeDB{}
	rows, err := Bounded(f, time.Minute).Query(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	if n != 2 || rows.Err() != nil {
		t.Fatalf("read %d rows, err %v; want 2 and nil", n, rows.Err())
	}
	rows.Close()
	if f.ctxs[0].Err() == nil {
		t.Fatal("query context still live after rows.Close")
	}
}

func TestBoundedQueryRowKeepsItsContextUntilScan(t *testing.T) {
	f := &fakeDB{}
	row := Bounded(f, time.Minute).QueryRow(context.Background(), "x")
	if err := row.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if f.ctxs[0].Err() == nil {
		t.Fatal("row context still live after Scan")
	}
}

func TestBoundedWithTimeoutChangesOnlyTheBound(t *testing.T) {
	f := &fakeDB{}
	b := Bounded(f, time.Hour).WithTimeout(time.Second)
	if _, err := b.Exec(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if dl, _ := f.ctxs[0].Deadline(); time.Until(dl) > time.Second {
		t.Fatalf("deadline %v, want within a second", dl)
	}
}
