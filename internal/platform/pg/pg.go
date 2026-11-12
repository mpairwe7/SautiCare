// Package pg holds the PostgreSQL adapters' shared pieces: the five store
// names and connection-pool construction. Each store is a separate database
// with its own owner and runtime roles (spec §6.1, ADR-0006).
package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store names one of the five separate data stores.
type Store string

const (
	StoreOperational Store = "operational"
	StoreAudit       Store = "audit"
	StoreAnalytic    Store = "analytic"
	StoreLinkage     Store = "linkage"
	StoreMedia       Store = "media"
)

// Stores lists all five stores in a stable order.
func Stores() []Store {
	return []Store{StoreOperational, StoreAudit, StoreAnalytic, StoreLinkage, StoreMedia}
}

// Owner returns the migration-owner role of a store.
func (s Store) Owner() string {
	return map[Store]string{
		StoreOperational: "op_owner", StoreAudit: "audit_owner", StoreAnalytic: "an_owner",
		StoreLinkage: "lk_owner", StoreMedia: "md_owner",
	}[s]
}

// RuntimeRoles returns the least-privilege login roles of a store.
func (s Store) RuntimeRoles() []string {
	switch s {
	case StoreOperational:
		return []string{"op_intake", "op_ivr", "op_dispatch", "op_console", "op_payments",
			"op_relay", "op_pseudonymiser", "op_retention", "op_readonly"}
	case StoreAudit:
		return []string{"audit_appender", "audit_reader"}
	case StoreAnalytic:
		return []string{"an_writer", "an_reader"}
	case StoreLinkage:
		return []string{"lk_writer", "lk_reader"}
	case StoreMedia:
		return []string{"md_writer", "md_reader"}
	}
	return nil
}

// Connect opens a pool with a statement timeout, so no query on the dispatch
// path can hang indefinitely (spec §7.5), and an application_name for
// pg_stat_activity.
func Connect(ctx context.Context, dsn, appName string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.RuntimeParams["application_name"] = appName
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	cfg.HealthCheckPeriod = 10 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	return pool, nil
}
