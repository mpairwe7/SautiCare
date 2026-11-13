// Package db embeds the forward-only SQL migrations of the five stores
// (spec §6.5). Each store's directory holds migrations/NNNN_name.sql and,
// where sqlc is used, queries/*.sql.
package db

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed operational/migrations/*.sql audit/migrations/*.sql analytic/migrations/*.sql linkage/migrations/*.sql media/migrations/*.sql
var all embed.FS

// Migrations returns the store's migration tree, rooted so that the files
// live under "migrations/".
func Migrations(store string) (fs.FS, error) {
	sub, err := fs.Sub(all, store)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	if _, err := fs.ReadDir(sub, "migrations"); err != nil {
		return nil, fmt.Errorf("db: unknown store %q: %w", store, err)
	}
	return sub, nil
}
