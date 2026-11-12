// Package pgtest gives integration tests a real PostgreSQL 18 server.
//
// If SAUTI_TEST_PG_DSN is set (CI: an official postgres:18.6 service
// container) it is used as the superuser DSN. Otherwise an embedded
// PostgreSQL 18.6 is started once per test binary; packages call
// pgtest.Main from TestMain so it is stopped afterwards (ADR-0012).
//
// Every Cluster call bootstraps the five stores under a random database
// suffix and migrates them, so tests never share state.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"
	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/db"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/bootstrap"
	"sautihealth.com/sauti/internal/platform/pg/migrate"
)

// Deadlines for the harness's own database calls, so a held advisory lock
// or a stalled server fails the test promptly instead of hanging it until
// the test binary times out.
const (
	setupTimeout   = 2 * time.Minute  // bootstrap and migration of one cluster
	connectTimeout = 30 * time.Second // one connection, or one statement on it
)

var (
	once         sync.Once
	adminURL     string
	rolePassword string
	startErr     error
	server       *embeddedpostgres.EmbeddedPostgres
	workDir      string
)

func randomSecret() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Main runs the tests and stops the embedded server, if one was started.
func Main(m *testing.M) {
	code := m.Run()
	if server != nil {
		_ = server.Stop()
	}
	if workDir != "" {
		_ = os.RemoveAll(workDir)
	}
	os.Exit(code)
}

func freePort() (uint32, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil //nolint:gosec // TCP ports fit in 16 bits
}

func start() {
	if v := os.Getenv("SAUTI_TEST_PG_DSN"); v != "" {
		// A shared server: roles are server-global, so every test process
		// must use the same role password (CI derives it from the run ID).
		rolePassword = os.Getenv("SAUTI_TEST_ROLE_PASSWORD")
		if rolePassword == "" {
			startErr = fmt.Errorf("pgtest: SAUTI_TEST_ROLE_PASSWORD is required with SAUTI_TEST_PG_DSN (roles are shared by all test processes)")
			return
		}
		adminURL = v
		return
	}
	// A private embedded server: random credentials for this process only.
	rolePassword = randomSecret()
	superPassword := randomSecret()
	port, err := freePort()
	if err != nil {
		startErr = err
		return
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	workDir, err = os.MkdirTemp("", "sauti-pgtest-")
	if err != nil {
		startErr = err
		return
	}
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		Port(port).
		Username("postgres").
		Password(superPassword).
		Database("postgres").
		CachePath(filepath.Join(cache, "sauti", "embedded-postgres")).
		RuntimePath(filepath.Join(workDir, "runtime")).
		DataPath(filepath.Join(workDir, "data")).
		StartTimeout(90 * time.Second).
		Logger(io.Discard)
	server = embeddedpostgres.NewDatabase(cfg)
	if err := server.Start(); err != nil {
		startErr = fmt.Errorf("pgtest: start embedded PostgreSQL: %w", err)
		return
	}
	adminURL = (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", superPassword),
		Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/postgres", RawQuery: "sslmode=disable"}).String()
}

// RolePassword returns the login password of every Sauti role on the test server.
func RolePassword(tb testing.TB) string {
	tb.Helper()
	AdminURL(tb)
	return rolePassword
}

// AdminURL returns the superuser DSN of the test server.
func AdminURL(tb testing.TB) string {
	tb.Helper()
	once.Do(start)
	if startErr != nil {
		tb.Fatal(startErr)
	}
	return adminURL
}

func withUserDB(base, user, password, dbname string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic(fmt.Sprintf("pgtest: admin DSN must be a URL: %v", err))
	}
	u.User = url.UserPassword(user, password)
	u.Path = "/" + dbname
	return u.String()
}

func connect(tb testing.TB, dsn string) *pgx.Conn {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		tb.Fatalf("pgtest: connect: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

// AdminConn returns a superuser connection to the maintenance database.
func AdminConn(tb testing.TB) *pgx.Conn {
	tb.Helper()
	return connect(tb, AdminURL(tb))
}

// RandomSuffix returns a database-name suffix unique to one test.
func RandomSuffix() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return "_t" + hex.EncodeToString(b)
}

// EmptyDB creates an empty database owned by the superuser and returns its DSN.
func EmptyDB(tb testing.TB) string {
	tb.Helper()
	admin := AdminConn(tb)
	name := "sauti_empty" + RandomSuffix()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { dropDB(tb, name) })
	u, _ := url.Parse(AdminURL(tb))
	u.Path = "/" + name
	return u.String()
}

func dropDB(tb testing.TB, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	c, err := pgx.Connect(ctx, AdminURL(tb))
	if err != nil {
		return
	}
	defer func() { _ = c.Close(ctx) }()
	_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
}

// DropDatabases drops bootstrap-created databases.
func DropDatabases(tb testing.TB, dbs map[pg.Store]string) {
	for _, name := range dbs {
		dropDB(tb, name)
	}
}

// TestCluster is one isolated, migrated five-store environment.
type TestCluster struct {
	admin string
	dbs   map[pg.Store]string
}

// Cluster bootstraps and migrates the five stores for one test.
func Cluster(tb testing.TB) *TestCluster {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	admin := AdminConn(tb)
	dbs, err := bootstrap.Run(ctx, admin, bootstrap.Options{Suffix: RandomSuffix(), Password: RolePassword(tb), Env: "test"})
	if err != nil {
		tb.Fatalf("pgtest: bootstrap: %v", err)
	}
	tb.Cleanup(func() { DropDatabases(tb, dbs) })
	c := &TestCluster{admin: AdminURL(tb), dbs: dbs}
	for _, s := range pg.Stores() {
		fsys, err := db.Migrations(string(s))
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := migrate.Run(ctx, c.Owner(tb, s), string(s), fsys); err != nil {
			tb.Fatalf("pgtest: migrate %s: %v", s, err)
		}
	}
	return c
}

// Database returns the database name of a store.
func (c *TestCluster) Database(s pg.Store) string { return c.dbs[s] }

// DSN returns a DSN for role connecting to store's database.
func (c *TestCluster) DSN(s pg.Store, role string) string {
	return withUserDB(c.admin, role, rolePassword, c.dbs[s])
}

// Owner returns a connection as the store's owner role.
func (c *TestCluster) Owner(tb testing.TB, s pg.Store) *pgx.Conn {
	tb.Helper()
	return connect(tb, c.DSN(s, s.Owner()))
}

// Superuser returns a superuser connection to the store's database, for
// tests that simulate a malicious administrator (tamper detection).
func (c *TestCluster) Superuser(tb testing.TB, s pg.Store) *pgx.Conn {
	tb.Helper()
	u, _ := url.Parse(c.admin)
	u.Path = "/" + c.dbs[s]
	return connect(tb, u.String())
}
