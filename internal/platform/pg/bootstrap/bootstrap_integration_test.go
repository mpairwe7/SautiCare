//go:build integration

package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/bootstrap"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// Five stores, five credential sets: a runtime role of one store cannot even
// connect to another store's database (hard constraint 9).
func TestBootstrapIsolatesStores(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	cross, err := pgx.Connect(ctx, c.DSN(pg.StoreAudit, "op_intake"))
	if err == nil {
		cross.Close(ctx)
		t.Fatal("op_intake connected to the audit store")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-store connect error = %v, want SQLSTATE 42501", err)
	}
	own, err := pgx.Connect(ctx, c.DSN(pg.StoreOperational, "op_intake"))
	if err != nil {
		t.Fatalf("op_intake cannot connect to its own store: %v", err)
	}
	own.Close(ctx)
}

func TestBootstrapRefusesProduction(t *testing.T) {
	admin := pgtest.AdminConn(t)
	_, err := bootstrap.Run(context.Background(), admin, bootstrap.Options{Suffix: "_prod", Password: "x", Env: "production"})
	if !errors.Is(err, bootstrap.ErrProductionForbidden) {
		t.Fatalf("err = %v, want ErrProductionForbidden", err)
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	admin := pgtest.AdminConn(t)
	ctx := context.Background()
	opts := bootstrap.Options{Suffix: pgtest.RandomSuffix(), Password: pgtest.Password, Env: "test"}
	first, err := bootstrap.Run(ctx, admin, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pgtest.DropDatabases(t, first) })
	second, err := bootstrap.Run(ctx, admin, opts)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if len(second) != len(pg.Stores()) || second[pg.StoreOperational] != first[pg.StoreOperational] {
		t.Fatalf("second = %v, first = %v", second, first)
	}
}
