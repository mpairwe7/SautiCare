package opstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

// verifyTimeout bounds a whole verification run: an operator task that reads
// every incident's events and projection in one snapshot.
var verifyTimeout = 30 * time.Minute

// verifyHook is a test seam: called between reading events and projection.
var verifyHook func()

// VerifyProjections replays every incident's events through the pure
// reducer and compares the result with the database projection (spec §6.2).
// Everything is read in one REPEATABLE READ, READ ONLY transaction, so an
// append landing mid-run on a live system is never reported as drift
// (review finding M3). It returns how many incidents were checked and one
// line per drift.
func VerifyProjections(ctx context.Context, s *Store) (int, []string, error) {
	// The whole run has a deadline: every snapshot statement inherits it,
	// even when the caller's context has none (cancel runs after rollback).
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, nil, fmt.Errorf("opstore: verify snapshot: %w", err)
	}
	defer rollback(ctx, tx)
	snap := &Store{pool: s.pool, q: opdb.New(tx)}

	ids, err := snap.ListIncidentIDs(ctx)
	if err != nil {
		return 0, nil, err
	}
	var drift []string
	for _, id := range ids {
		events, err := snap.Events(ctx, id)
		if err != nil {
			return 0, nil, err
		}
		want, err := incident.Fold(events)
		if err != nil {
			drift = append(drift, fmt.Sprintf("%s: events do not fold: %v", id, err))
			continue
		}
		if verifyHook != nil {
			verifyHook()
		}
		got, err := snap.Projection(ctx, id)
		if err != nil {
			return 0, nil, err
		}
		if d := incident.Diff(got, want); d != "" {
			drift = append(drift, fmt.Sprintf("%s: %s", id, d))
		}
	}
	return len(ids), drift, nil
}
