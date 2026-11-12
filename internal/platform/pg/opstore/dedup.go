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

// prefilterSeconds is the dedup window in whole seconds for the SQL
// candidate prefilter, rounded up: the prefilter only narrows the
// candidates and DedupRule.Survivor applies the exact window, so it must
// never drop a call a fractional window (15m500ms) would accept.
func prefilterSeconds(w time.Duration) (int32, error) {
	s := w / time.Second
	if w%time.Second > 0 {
		s++
	}
	return i32(int(s))
}

// DispatchOrMerge implements intake.Dispatcher. In one transaction, under an
// advisory lock: read the candidates FOR SHARE (op.dedup_candidates), so a
// concurrent closure, merge or split of a candidate is waited for and seen,
// and none can happen before this commits; append dispatch_ready (T2); if the
// rule finds a survivor, append merged on this incident and merge_absorbed on
// the survivor, plus the call's higher severity or escalation on the survivor
// (merge). A retried request whose dispatch_ready already exists reports the
// stored outcome and decides nothing again.
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
	window, err := prefilterSeconds(r.Rule.Window)
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
		cands = append(cands, intake.Candidate{ID: uuid.UUID(c.ID.Bytes), Category: c.Category,
			Zone: incident.Zone{Col: int(c.ZoneCol), Row: int(c.ZoneRow)}, LocationQuality: c.LocationQuality,
			Severity: incident.Severity(c.Severity), Escalated: c.Escalated, T1: c.T1.Time, T2: c.T2.Time})
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
	survivor, ok := r.Rule.Survivor(r.Call(), cands)
	if ok {
		if err = ts.merge(ctx, r, survivor); err != nil {
			return out, err
		}
		out = intake.DispatchOutcome{Merged: true, Survivor: survivor.ID}
	}
	if err = tx.Commit(ctx); err != nil {
		return intake.DispatchOutcome{}, err
	}
	return out, nil
}

// merge records, in the caller's transaction, the call's merge into
// survivor: merged on the call, merge_absorbed on the survivor, and what the
// call carries to it (intake.Consequences): severity_raised if the call is
// more urgent, an escalation if the call was escalated and the survivor is
// not. A merge never leaves the dispatch less urgent than a call it absorbed.
func (s *Store) merge(ctx context.Context, r intake.DispatchRequest, survivor intake.Candidate) error {
	into, err := json.Marshal(map[string]string{"into": survivor.ID.String()})
	if err != nil {
		return err
	}
	absorbed, err := json.Marshal(map[string]string{"from": r.IncidentID.String(), "t1": r.T1.UTC().Format(t1Layout)})
	if err != nil {
		return err
	}
	cons, err := intake.Consequences(r.Call(), survivor)
	if err != nil {
		return err
	}
	from := r.IncidentID.String()
	events := []incident.AppendParams{
		{IncidentID: r.IncidentID, Type: incident.EvMerged, Payload: into, IdemKey: r.IdemKey + ":merge"},
		{IncidentID: survivor.ID, Type: incident.EvMergeAbsorbed, Payload: absorbed, IdemKey: "dedup:absorb:" + from},
	}
	if cons.Raise != nil {
		events = append(events, incident.AppendParams{IncidentID: survivor.ID, Type: incident.EvSeverityRaised, Payload: cons.Raise,
			IdemKey: "dedup:raise:" + from})
	}
	if cons.Escalate != nil {
		events = append(events, incident.AppendParams{IncidentID: survivor.ID, Type: incident.EvEscalated, Payload: cons.Escalate,
			IdemKey: "dedup:escalate:" + from})
	}
	for _, e := range events {
		e.ActorKind = incident.ActorSystem
		if _, err := s.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
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
