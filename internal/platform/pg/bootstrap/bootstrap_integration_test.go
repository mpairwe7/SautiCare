//go:build integration

package bootstrap_test

import (
	"context"
	"errors"
	"net/url"
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

// CodeRabbit PR #3: PostgreSQL grants CONNECT to PUBLIC on a new database.
// A bootstrap that fails between CREATE DATABASE and the database's grants
// must not leave it open to another store's roles; a rerun completes it.
func TestFailedBootstrapLeavesNewDatabaseClosed(t *testing.T) {
	admin := pgtest.AdminConn(t)
	ctx := context.Background()
	opts := bootstrap.Options{Suffix: pgtest.RandomSuffix(), Password: pgtest.RolePassword(t), Env: "test"}
	name := bootstrap.DatabaseName(pg.StoreOperational, opts.Suffix)
	t.Cleanup(func() { pgtest.DropDatabases(t, map[pg.Store]string{pg.StoreOperational: name}) })
	dsn := func(role string) string {
		u, err := url.Parse(pgtest.AdminURL(t))
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword(role, opts.Password)
		u.Path = "/" + name
		return u.String()
	}
	boom := errors.New("injected failure after CREATE DATABASE")
	restore := bootstrap.SetAfterCreateHook(func(db string) error {
		if db == name {
			return boom
		}
		return nil
	})
	defer restore()
	if _, err := bootstrap.Run(ctx, admin, opts); !errors.Is(err, boom) {
		t.Fatalf("bootstrap err = %v, want the injected failure", err)
	}
	for _, role := range []string{"audit_appender", "an_writer", "lk_reader", "md_writer", "op_intake"} {
		if c, err := pgx.Connect(ctx, dsn(role)); err == nil {
			_ = c.Close(ctx)
			t.Errorf("%s connected to %s left by a failed bootstrap", role, name)
		}
	}

	restore()
	dbs, err := bootstrap.Run(ctx, admin, opts)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	t.Cleanup(func() { pgtest.DropDatabases(t, dbs) })
	own, err := pgx.Connect(ctx, dsn("op_intake"))
	if err != nil {
		t.Fatalf("op_intake cannot connect to its own store after the rerun: %v", err)
	}
	_ = own.Close(ctx)
	if c, err := pgx.Connect(ctx, dsn("audit_appender")); err == nil {
		_ = c.Close(ctx)
		t.Fatal("audit_appender connected to the operational store after the rerun")
	}
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
	opts := bootstrap.Options{Suffix: pgtest.RandomSuffix(), Password: pgtest.RolePassword(t), Env: "test"}
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
