//go:build integration

package opstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// CodeRabbit PR #3 (review of ead62cf), finding 1: a flag is a JSON string.
// The write path reads the flag with ->>, which turns {"flag":123} into the
// non-empty text "123" and accepts it; incident.Apply decodes the flag into
// a Go string and rejects the stored event, so Fold could no longer replay
// the history. A CHECK constraint on op.incident_events (0027) rejects it
// below every version of the write path.
func TestFlagEventNeedsAJSONStringFlag(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := pgtest.Context(t)
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"flag":123}`, `{"flag":true}`, `{"flag":["test_call"]}`, `{"flag":{"test_call":1}}`} {
		if err := appendErr(s, o.IncidentID, incident.EvFlagged, bad, 0); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("flag event %s: err = %v, want ErrIllegalTransition", bad, err)
		}
	}
	appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"test_call"}`, "")
	// Added NOT VALID: no scan of existing rows when the migration runs (it
	// cannot fail on, or lock the table for, events stored before it); new
	// writes are checked; validating existing rows is a separate operator
	// step (docs/runbooks/database-migrations.md).
	var validated bool
	if err := c.Owner(t, pg.StoreOperational).QueryRow(ctx,
		"SELECT convalidated FROM pg_constraint WHERE conname = 'incident_events_flag_is_string' AND conrelid = 'op.incident_events'::regclass").
		Scan(&validated); err != nil || validated {
		t.Fatalf("constraint validated at migration time: %v %v", validated, err)
	}
	evs, err := s.Events(ctx, o.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := incident.Fold(evs); err != nil || len(p.Flags) != 1 || p.Flags[0] != "test_call" {
		t.Fatalf("stored history folds to flags %v, err %v; want [test_call]", p.Flags, err)
	}

	// The rule is the table's, not the write path's: even the owner, writing
	// below the write path, cannot store a non-string flag. Only flag events
	// carry a flag; another event type is unaffected.
	owner := c.Owner(t, pg.StoreOperational)
	const direct = `INSERT INTO op.incident_events (incident_id, seq, type, actor_kind, payload, idem_key)
		VALUES ($1, $2, $3, 'system', $4::jsonb, $5)`
	if _, err := owner.Exec(ctx, direct, o.IncidentID, 1000, "incident.flagged", `{"flag":123}`, "direct-flag"); pgCode(err) != "23514" {
		t.Errorf("owner insert of a numeric flag: err = %v, want check violation 23514", err)
	}
	if _, err := owner.Exec(ctx, direct, o.IncidentID, 1001, "incident.flagged", `{"note":"no flag"}`, "direct-none"); pgCode(err) != "23514" {
		t.Errorf("owner insert of a flag event without a flag: err = %v, want check violation 23514", err)
	}
	if _, err := owner.Exec(ctx, direct, o.IncidentID, 1002, "incident.escalated", `{"reason":"test","flag":123}`, "direct-other"); err != nil {
		t.Errorf("owner insert of another event type with a non-string flag key: %v", err)
	}
}

// Finding 10: every call the adapter makes has its own deadline, pool wait
// included. The server stops a statement after 5 s and an idle transaction
// after 30 s, but nothing bounded the wait for a pool connection when the
// caller's context had no deadline. A transaction is bounded only while it
// begins: an outbox batch holds it until Complete or Abort.
func TestOpstoreCallsHaveTheirOwnDeadline(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	pool, err := pg.Connect(ctx, c.DSN(pg.StoreOperational, "op_intake"), "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	held, err := pool.Acquire(ctx) // the only connection: the pool is exhausted
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	defer opstore.SetOpTimeout(200 * time.Millisecond)()
	s := opstore.New(pool)
	params, id := openParams(t), uuid.NewV7()
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"OpenIncident", func(ctx context.Context) error { _, err := s.OpenIncident(ctx, params); return err }},
		{"Append", func(ctx context.Context) error {
			_, err := s.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvFlagged,
				Payload: json.RawMessage(`{"flag":"test_call"}`), ActorKind: incident.ActorSystem, IdemKey: "k"})
			return err
		}},
		{"Events", func(ctx context.Context) error { _, err := s.Events(ctx, id); return err }},
		{"Projection", func(ctx context.Context) error { _, err := s.Projection(ctx, id); return err }},
		{"ListIncidentIDs", func(ctx context.Context) error { _, err := s.ListIncidentIDs(ctx); return err }},
		{"DefaultPartitionRows", func(ctx context.Context) error { _, err := s.DefaultPartitionRows(ctx); return err }},
		{"Claim", func(ctx context.Context) error {
			b, err := s.Claim(ctx, "audit", 1)
			if err == nil {
				b.Abort(ctx)
			}
			return err
		}},
		{"VerifyProjections", func(ctx context.Context) error { _, _, err := opstore.VerifyProjections(ctx, s); return err }},
	} {
		done := make(chan error, 1)
		// Deliberately a caller without a deadline: the store must add one.
		go func() { done <- tc.call(context.Background()) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s on an exhausted pool: err = %v, want context.DeadlineExceeded", tc.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s waited on an exhausted pool with no deadline", tc.name)
		}
	}
}

// The bound covers Claim's Begin only: the batch's transaction outlives it.
func TestClaimedBatchOutlivesThePerCallDeadline(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	defer opstore.SetOpTimeout(200 * time.Millisecond)()
	if _, err := storeAs(t, c, "op_intake").OpenIncident(ctx, openParams(t)); err != nil {
		t.Fatal(err)
	}
	b, err := storeAs(t, c, "op_relay").Claim(ctx, "audit", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort(ctx)
	time.Sleep(500 * time.Millisecond) // well past the per-call bound
	ids := make([]uuid.UUID, 0, len(b.Rows()))
	for _, r := range b.Rows() {
		ids = append(ids, r.ID)
	}
	if len(ids) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(ids))
	}
	if err := b.Complete(ctx, ids); err != nil {
		t.Fatalf("completing a batch held past the per-call bound: %v", err)
	}
}

// CodeRabbit PR #3 (second round): the statements on a claimed batch's held
// transaction are bounded one by one — a claim stuck behind a lock returns
// at the per-call deadline, not at the server's statement timeout — while
// the batch itself may stay open (TestClaimedBatchOutlivesThePerCallDeadline).
func TestClaimStatementsHaveTheirOwnDeadline(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	defer opstore.SetOpTimeout(300 * time.Millisecond)()
	if _, err := storeAs(t, c, "op_intake").OpenIncident(ctx, openParams(t)); err != nil {
		t.Fatal(err)
	}
	relay := storeAs(t, c, "op_relay")
	tx, err := c.Owner(t, pg.StoreOperational).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "LOCK TABLE op.outbox IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	b, err := relay.Claim(context.Background(), "audit", 10)
	if err == nil {
		b.Abort(ctx)
	}
	if took := time.Since(start); !errors.Is(err, context.DeadlineExceeded) || took > 2*time.Second {
		t.Fatalf("a claim stuck behind a lock: %v after %v, want context.DeadlineExceeded at the per-call bound", err, took)
	}
}

// A verification run has a deadline of its own: every snapshot statement
// inherits it, even when the caller's context has none.
func TestVerifyProjectionsHasARunDeadline(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	s := storeAs(t, c, "op_intake")
	if _, err := s.OpenIncident(ctx, openParams(t)); err != nil {
		t.Fatal(err)
	}
	defer opstore.SetVerifyTimeout(300 * time.Millisecond)()
	defer opstore.SetVerifyHook(func() { time.Sleep(500 * time.Millisecond) })()
	if _, _, err := opstore.VerifyProjections(context.Background(), storeAs(t, c, "op_readonly")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a verification run past its deadline: %v, want context.DeadlineExceeded", err)
	}
}
