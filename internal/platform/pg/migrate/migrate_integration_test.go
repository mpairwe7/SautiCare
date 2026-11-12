//go:build integration

package migrate_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/platform/pg/migrate"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func conn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func files() fstest.MapFS {
	return fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("CREATE TABLE a (x int PRIMARY KEY);")},
		"migrations/0002_b.sql": {Data: []byte("CREATE TABLE b (y int REFERENCES a(x));\nCREATE INDEX b_y ON b(y);")},
	}
}

func TestMigrateAppliesInOrderOnce(t *testing.T) {
	c := conn(t, pgtest.EmptyDB(t))
	ctx := context.Background()
	got, err := migrate.Run(ctx, c, "test", files())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "0001_a" || got[1] != "0002_b" {
		t.Fatalf("applied = %v", got)
	}
	again, err := migrate.Run(ctx, c, "test", files())
	if err != nil || len(again) != 0 {
		t.Fatalf("second run applied %v, err %v", again, err)
	}
}

// An applied migration is a historical record; editing it must stop the
// migrator rather than silently diverge from what production ran.
func TestMigrateRefusesChangedChecksum(t *testing.T) {
	c := conn(t, pgtest.EmptyDB(t))
	ctx := context.Background()
	if _, err := migrate.Run(ctx, c, "test", files()); err != nil {
		t.Fatal(err)
	}
	edited := files()
	edited["migrations/0001_a.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE a (x bigint PRIMARY KEY);")}
	if _, err := migrate.Run(ctx, c, "test", edited); !errors.Is(err, migrate.ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
}

func TestMigrateSerializesConcurrentRunners(t *testing.T) {
	dsn := pgtest.EmptyDB(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([][]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		c := conn(t, dsn)
		wg.Go(func() { results[i], errs[i] = migrate.Run(ctx, c, "test", files()) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent run error: %v", err)
		}
	}
	if total := len(results[0]) + len(results[1]); total != 2 {
		t.Fatalf("files applied in total = %d, want 2 (%v)", total, results)
	}
}

func TestMigrateRollsBackFailedFile(t *testing.T) {
	c := conn(t, pgtest.EmptyDB(t))
	ctx := context.Background()
	bad := fstest.MapFS{"migrations/0001_bad.sql": {Data: []byte("CREATE TABLE ok1 (x int);\nSELECT no_such_function();")}}
	if _, err := migrate.Run(ctx, c, "test", bad); err == nil {
		t.Fatal("failing migration reported success")
	}
	var n int
	if err := c.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE tablename = 'ok1'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial migration left table behind (n=%d, err=%v)", n, err)
	}
}

func TestMigrateRejectsBadFileName(t *testing.T) {
	c := conn(t, pgtest.EmptyDB(t))
	bad := fstest.MapFS{"migrations/1_x.sql": {Data: []byte("SELECT 1;")}}
	if _, err := migrate.Run(context.Background(), c, "test", bad); err == nil {
		t.Fatal("badly named migration accepted")
	}
}
