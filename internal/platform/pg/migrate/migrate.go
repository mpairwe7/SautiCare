// Package migrate is Sauti's forward-only schema migrator (ADR-0010).
//
// Each store embeds its migrations as migrations/NNNN_name.sql. A migration
// runs once, in its own transaction, under a session advisory lock so
// concurrent deploys serialise. The sha256 of every applied file is recorded;
// if an applied file is later edited or removed the migrator refuses to run,
// before applying anything, because the database would no longer match what
// its history says it ran. Problems are fixed forward with a new migration —
// there are no down migrations.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrChecksumMismatch reports an applied migration whose file has changed.
var ErrChecksumMismatch = errors.New("migrate: applied migration file has changed")

// ErrMissingMigration reports an applied migration whose file is gone.
var ErrMissingMigration = errors.New("migrate: applied migration file is missing")

var fileName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

const ledgerDDL = `CREATE TABLE IF NOT EXISTS public.schema_migrations (
	version    text PRIMARY KEY,
	sha256     text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
)`

type file struct {
	version, sha, body string
}

func load(fsys fs.FS) ([]file, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	var out []file
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !fileName.MatchString(e.Name()) {
			return nil, fmt.Errorf("migrate: bad migration file name %q (want NNNN_name.sql)", e.Name())
		}
		b, err := fs.ReadFile(fsys, path.Join("migrations", e.Name()))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, file{
			version: strings.TrimSuffix(e.Name(), ".sql"),
			sha:     hex.EncodeToString(sum[:]),
			body:    string(b),
		})
	}
	slices.SortFunc(out, func(a, b file) int { return strings.Compare(a.version, b.version) })
	return out, nil
}

// Run applies pending migrations from fsys to the database behind conn and
// returns the versions it applied, in order.
func Run(ctx context.Context, conn *pgx.Conn, store string, fsys fs.FS) ([]string, error) {
	files, err := load(fsys)
	if err != nil {
		return nil, err
	}
	lockKey := "sauti-migrate:" + store
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext($1))", lockKey); err != nil {
		return nil, fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() {
		// Unlock on a fresh, bounded context so a cancelled ctx cannot leak
		// the lock and a stalled connection cannot hang the migrator.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(uctx, "SELECT pg_advisory_unlock(hashtext($1))", lockKey)
	}()
	if _, err := conn.Exec(ctx, ledgerDDL); err != nil {
		return nil, fmt.Errorf("migrate: ledger: %w", err)
	}
	applied := map[string]string{}
	rows, err := conn.Query(ctx, "SELECT version, sha256 FROM public.schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: read ledger: %w", err)
	}
	for rows.Next() {
		var v, s string
		if err := rows.Scan(&v, &s); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Check the whole recorded history against the files before applying
	// anything: a pending file must never run when an applied one is gone
	// or has changed.
	shaOf := make(map[string]string, len(files))
	for _, f := range files {
		shaOf[f.version] = f.sha
	}
	for _, v := range slices.Sorted(maps.Keys(applied)) {
		sha, ok := shaOf[v]
		if !ok {
			return nil, fmt.Errorf("%w: %s/%s", ErrMissingMigration, store, v)
		}
		if sha != applied[v] {
			return nil, fmt.Errorf("%w: %s/%s", ErrChecksumMismatch, store, v)
		}
	}

	var done []string
	for _, f := range files {
		if _, ok := applied[f.version]; ok {
			continue
		}
		if err := apply(ctx, conn, f); err != nil {
			return done, fmt.Errorf("migrate: %s/%s: %w", store, f.version, err)
		}
		done = append(done, f.version)
	}
	return done, nil
}

func apply(ctx context.Context, conn *pgx.Conn, f file) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// No arguments: pgx uses the simple protocol, so a file may hold many statements.
	if _, err := tx.Exec(ctx, f.body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO public.schema_migrations (version, sha256) VALUES ($1, $2)", f.version, f.sha); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
