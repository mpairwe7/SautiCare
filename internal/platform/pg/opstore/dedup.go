package opstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

// dedupLockKey serialises dedup decisions: two calls completing at the same
// moment cannot both become survivors (or both merge into each other).
const dedupLockKey = 7347010

// t1Layout is RFC 3339 with at most microseconds (the database precision and
// the merge_absorbed format).
const t1Layout = "2006-01-02T15:04:05.999999Z07:00"

func i32(v int) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("opstore: %d out of int32 range", v)
	}
	return int32(v), nil //nolint:gosec // G115: range checked above
}

// DispatchOrMerge implements intake.Dispatcher. In one transaction, under an
// advisory lock: append dispatch_ready (T2); if the rule finds a survivor,
// append merged on this incident and merge_absorbed on the survivor. A
// retried request whose dispatch_ready already exists reports the stored
// outcome and decides nothing again.
func (s *Store) DispatchOrMerge(ctx context.Context, r intake.DispatchRequest) (out intake.DispatchOutcome, err error) {
	col, err := i32(r.Zone.Col)
	if err != nil {
		return out, err
	}
	row, err := i32(r.Zone.Row)
	if err != nil {
		return out, err
	}
	dist, err := i32(r.Rule.MaxZoneDistance)
	if err != nil {
		return out, err
	}
	window, err := i32(int(r.Rule.Window / time.Second))
	if err != nil {
		return out, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(dedupLockKey)); err != nil {
		return out, fmt.Errorf("opstore: dedup lock: %w", err)
	}
	ts := &Store{pool: s.pool, q: opdb.New(tx)}
	rows, err := ts.q.DedupCandidates(ctx, opdb.DedupCandidatesParams{Category: r.Category, Col: col, Dist: dist, RowIndex: row,
		T1: pgtype.Timestamptz{Time: r.T1, Valid: true}, WindowSeconds: window, ID: pgID(r.IncidentID)})
	if err != nil {
		return out, fmt.Errorf("opstore: dedup candidates: %w", err)
	}
	cands := make([]intake.Candidate, 0, len(rows))
	for _, c := range rows {
		if c.ZoneCol == nil || c.ZoneRow == nil {
			continue
		}
		cands = append(cands, intake.Candidate{ID: uuid.UUID(c.ID.Bytes), Category: deref(c.Category),
			Zone: incident.Zone{Col: int(*c.ZoneCol), Row: int(*c.ZoneRow)}, T1: c.T1.Time, T2: c.T2.Time})
	}
	a, err := ts.Append(ctx, incident.AppendParams{IncidentID: r.IncidentID, Type: incident.EvDispatchReady, Payload: r.Payload,
		ActorKind: incident.ActorSystem, IdemKey: r.IdemKey})
	if err != nil {
		return out, err
	}
	if a.Duplicate {
		p, perr := ts.Projection(ctx, r.IncidentID)
		if perr != nil {
			return out, perr
		}
		if p.MergedInto != nil {
			out = intake.DispatchOutcome{Merged: true, Survivor: *p.MergedInto}
		}
		return out, tx.Commit(ctx)
	}
	survivor, ok := r.Rule.Survivor(r.IncidentID, r.Category, r.Zone, r.T1, cands)
	if ok {
		into, _ := json.Marshal(map[string]string{"into": survivor.String()})
		if _, err = ts.Append(ctx, incident.AppendParams{IncidentID: r.IncidentID, Type: incident.EvMerged, Payload: into,
			ActorKind: incident.ActorSystem, IdemKey: r.IdemKey + ":merge"}); err != nil {
			return out, err
		}
		absorbed, _ := json.Marshal(map[string]string{"from": r.IncidentID.String(), "t1": r.T1.UTC().Format(t1Layout)})
		if _, err = ts.Append(ctx, incident.AppendParams{IncidentID: survivor, Type: incident.EvMergeAbsorbed, Payload: absorbed,
			ActorKind: incident.ActorSystem, IdemKey: "dedup:absorb:" + r.IncidentID.String()}); err != nil {
			return out, err
		}
		out = intake.DispatchOutcome{Merged: true, Survivor: survivor}
	}
	if err = tx.Commit(ctx); err != nil {
		return intake.DispatchOutcome{}, err
	}
	return out, nil
}

// SplitIncident splits a wrongly merged call off its survivor (coordinator
// action; console role). Both events carry the staff identity; a retry is a
// no-op.
func (s *Store) SplitIncident(ctx context.Context, survivor, member, staff uuid.UUID) error {
	if err := s.q.SplitIncident(ctx, opdb.SplitIncidentParams{Survivor: pgID(survivor), Member: pgID(member), Staff: pgID(staff)}); err != nil {
		return mapErr(err)
	}
	return nil
}
