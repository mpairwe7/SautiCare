// Package bootstrap creates the five store databases and their roles on a
// development or CI PostgreSQL server (ADR-0006). In production the same
// layout is provisioned by Ansible and Patroni with credentials issued by
// OpenBao (P0); this package refuses to run there.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/platform/pg"
)

// ErrProductionForbidden is returned when Env is "production".
var ErrProductionForbidden = errors.New("bootstrap: dev bootstrap is forbidden in production")

// Options configures a bootstrap run.
type Options struct {
	Suffix   string // appended to database names (tests use a random suffix)
	Password string // login password for every role (dev/test only)
	Env      string // value of SAUTI_ENV; only "dev" or "test" may bootstrap
}

// roleLock serialises role creation across concurrent bootstraps on one server.
const roleLock = 7347000

// afterCreate is a test seam: Run calls it after CREATE DATABASE and before
// that database's grants (export_test.go).
var afterCreate func(db string) error

// DatabaseName returns the database name of store with suffix.
func DatabaseName(s pg.Store, suffix string) string { return "sauti_" + string(s) + suffix }

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Run creates (if missing) every role and the five databases, restricting
// CONNECT on each database to that store's roles. admin must be a superuser
// connection to a maintenance database. It returns store → database name.
//
// PostgreSQL grants CONNECT to PUBLIC on a new database, so each database is
// created with connections disabled and opened only after its grants are in
// place: a bootstrap that fails in between leaves a database nobody can
// connect to, never one any role can, and a rerun completes it.
func Run(ctx context.Context, admin *pgx.Conn, o Options) (map[pg.Store]string, error) {
	if o.Env != "dev" && o.Env != "test" {
		return nil, ErrProductionForbidden
	}
	if o.Password == "" {
		return nil, errors.New("bootstrap: password required")
	}
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", roleLock); err != nil {
		return nil, err
	}
	defer func() {
		// A fresh, bounded context: unlock even if ctx ended, never hang.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(uctx, "SELECT pg_advisory_unlock($1)", roleLock)
	}()

	for _, s := range pg.Stores() {
		for _, role := range append([]string{s.Owner()}, s.RuntimeRoles()...) {
			if err := ensureRole(ctx, admin, role, o.Password); err != nil {
				return nil, err
			}
		}
	}
	out := map[pg.Store]string{}
	for _, s := range pg.Stores() {
		db := DatabaseName(s, o.Suffix)
		var exists bool
		if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", db).Scan(&exists); err != nil {
			return nil, err
		}
		ident := pgx.Identifier{db}.Sanitize()
		if !exists {
			if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s ENCODING 'UTF8' TEMPLATE template0 ALLOW_CONNECTIONS false",
				ident, pgx.Identifier{s.Owner()}.Sanitize())); err != nil {
				return nil, fmt.Errorf("bootstrap: create %s: %w", db, err)
			}
			if afterCreate != nil {
				if err := afterCreate(db); err != nil {
					return nil, err
				}
			}
		}
		roles := []string{s.Owner()}
		roles = append(roles, s.RuntimeRoles()...)
		quoted := make([]string, len(roles))
		for i, r := range roles {
			quoted[i] = pgx.Identifier{r}.Sanitize()
		}
		stmts := []string{
			"REVOKE ALL ON DATABASE " + ident + " FROM PUBLIC",
			"GRANT CONNECT ON DATABASE " + ident + " TO " + strings.Join(quoted, ", "),
			"GRANT CREATE, TEMPORARY ON DATABASE " + ident + " TO " + pgx.Identifier{s.Owner()}.Sanitize(),
			"ALTER DATABASE " + ident + " WITH ALLOW_CONNECTIONS true", // only once the grants are in place
		}
		for _, q := range stmts {
			if _, err := admin.Exec(ctx, q); err != nil {
				return nil, fmt.Errorf("bootstrap: %s: %w", db, err)
			}
		}
		out[s] = db
	}
	return out, nil
}

func ensureRole(ctx context.Context, admin *pgx.Conn, role, password string) error {
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	q := fmt.Sprintf("%s ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD %s",
		verb, pgx.Identifier{role}.Sanitize(), quoteLiteral(password))
	if _, err := admin.Exec(ctx, q); err != nil {
		return fmt.Errorf("bootstrap: role %s: %w", role, err)
	}
	return nil
}
