//go:build integration

package auditstore_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgconn"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

func appenderStore(t *testing.T, c *pgtest.TestCluster, maxConns int32) *auditstore.Store {
	t.Helper()
	pool, err := pg.Connect(pgtest.Context(t), c.DSN(pg.StoreAudit, "audit_appender"), "test", maxConns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return auditstore.New(pool)
}

func readerStore(t *testing.T, c *pgtest.TestCluster) *auditstore.Store {
	t.Helper()
	pool, err := pg.Connect(pgtest.Context(t), c.DSN(pg.StoreAudit, "audit_reader"), "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return auditstore.New(pool)
}

func req(i int) audit.AppendRequest {
	inc := uuid.NewV7()
	r := audit.AppendRequest{
		EntryID:       uuid.NewV7(),
		OccurredAt:    time.Now().UTC(),
		EventType:     "screen.answered",
		IncidentID:    &inc,
		PayloadSHA256: sha256.Sum256([]byte(fmt.Sprint(i))),
	}
	if i%3 == 0 {
		r.LogicVersion = "tr-test+sha256:0123456789ab"
	}
	return r
}

func verifyChain(t *testing.T, r *auditstore.Store, wantN int64) {
	t.Helper()
	var prev [32]byte
	var n int64
	err := r.Range(pgtest.Context(t), 1, func(e audit.Entry) error {
		n++
		if e.Seq != n {
			return fmt.Errorf("seq %d at position %d", e.Seq, n)
		}
		if e.PrevHash != prev {
			return fmt.Errorf("seq %d prev_hash does not link", e.Seq)
		}
		if got := audit.ChainHash(prev, e); got != e.Hash {
			return fmt.Errorf("seq %d: SQL hash %x != Go hash %x", e.Seq, e.Hash, got)
		}
		prev = e.Hash
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != wantN {
		t.Fatalf("entries = %d, want %d", n, wantN)
	}
}

// The chain hash is computed in SQL; an independent Go implementation must
// reproduce every hash byte for byte.
func TestSQLHashMatchesGoCanonical(t *testing.T) {
	c := pgtest.Cluster(t)
	s := appenderStore(t, c, 2)
	ctx := pgtest.Context(t)
	for i := range 20 {
		if _, err := s.Append(ctx, req(i)); err != nil {
			t.Fatal(err)
		}
	}
	verifyChain(t, readerStore(t, c), 20)
	seq, head, err := s.Head(ctx)
	if err != nil || seq != 20 || head == ([32]byte{}) {
		t.Fatalf("Head = %d %x %v", seq, head, err)
	}
}

func TestHeadOfEmptyLedger(t *testing.T) {
	c := pgtest.Cluster(t)
	seq, head, err := appenderStore(t, c, 1).Head(pgtest.Context(t))
	if err != nil || seq != 0 || head != ([32]byte{}) {
		t.Fatalf("empty Head = %d %x %v", seq, head, err)
	}
}

// The relay delivers at least once; a redelivered entry must not create a
// second ledger row.
func TestAppendIdempotentOnEntryID(t *testing.T) {
	c := pgtest.Cluster(t)
	s := appenderStore(t, c, 1)
	ctx := pgtest.Context(t)
	r := req(1)
	a, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Append(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if a.Seq != b.Seq || a.Hash != b.Hash {
		t.Fatalf("redelivery created a new entry: %d vs %d", a.Seq, b.Seq)
	}
	verifyChain(t, readerStore(t, c), 1)
}

func TestConcurrentAppendsKeepValidChain(t *testing.T) {
	c := pgtest.Cluster(t)
	s := appenderStore(t, c, 20)
	ctx := pgtest.Context(t)
	var wg sync.WaitGroup
	errs := make(chan error, 500)
	for g := range 50 {
		wg.Go(func() {
			for i := range 10 {
				if _, err := s.Append(ctx, req(g*10+i)); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	verifyChain(t, readerStore(t, c), 500)
}

func pgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAppenderCannotUpdateOrDelete(t *testing.T) {
	c := pgtest.Cluster(t)
	s := appenderStore(t, c, 1)
	ctx := pgtest.Context(t)
	if _, err := s.Append(ctx, req(1)); err != nil {
		t.Fatal(err)
	}
	conn := pgtest.Connect(t, c.DSN(pg.StoreAudit, "audit_appender"))
	for _, q := range []string{
		"UPDATE audit.entries SET event_type = 'x.y.z'",
		"DELETE FROM audit.entries",
		"INSERT INTO audit.entries (seq, entry_id, occurred_at, event_type, payload_sha256, prev_hash, hash) VALUES (99, gen_random_uuid(), now(), 'a.b.c', '\\x00', '\\x00', '\\x00')",
		"SELECT * FROM audit.entries",
	} {
		if _, err := conn.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("%q as audit_appender: err = %v, want 42501", q, err)
		}
	}
}

// CodeRabbit PR #3: logic_version is an approved identifier
// (name+sha256:hex), never free text such as a name or a phone number.
func TestAppendRejectsFreeTextLogicVersion(t *testing.T) {
	c := pgtest.Cluster(t)
	s := appenderStore(t, c, 1)
	ctx := pgtest.Context(t)
	for _, bad := range []string{"Jane Doe +256700123456", "tr-test", "tr-test+sha256:00", "TR+sha256:0123456789ab",
		"tr test+sha256:0123456789ab", "tr-test+sha256:0123456789AB", "tr-test+sha256:0123456789ab\n"} {
		r := req(1)
		r.LogicVersion = bad
		if _, err := s.Append(ctx, r); pgCode(err) != "23514" {
			t.Errorf("logic_version %q: err = %v, want 23514", bad, err)
		}
	}
	for _, good := range []string{"", "tr-2026.11.0+sha256:0123456789ab", "tool1-pending+sha256:" + strings.Repeat("ab", 32)} {
		r := req(1)
		r.LogicVersion = good
		if _, err := s.Append(ctx, r); err != nil {
			t.Errorf("logic_version %q rejected: %v", good, err)
		}
	}
}

// CodeRabbit PR #3: every ledger call carries its own deadline, pool wait
// included (the statement timeout does not cover waiting for a connection),
// so neither the relay nor a verifier can block without limit on a stalled
// audit store, whatever context it passes. Range bounds each page query.
func TestEveryLedgerCallHasItsOwnDeadline(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	pool, err := pg.Connect(ctx, c.DSN(pg.StoreAudit, "audit_appender"), "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	held, err := pool.Acquire(ctx) // the only connection: the pool is exhausted
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	defer auditstore.SetCallTimeout(200 * time.Millisecond)()
	s := auditstore.New(pool)
	cp := audit.Checkpoint{Origin: "sauti-test", SignedAt: time.Now().UTC().Truncate(time.Microsecond), KeyID: "k1", Signature: []byte{1}}
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"Append", func(ctx context.Context) error { _, err := s.Append(ctx, req(1)); return err }},
		{"Head", func(ctx context.Context) error { _, _, err := s.Head(ctx); return err }},
		{"HashAt", func(ctx context.Context) error { _, _, err := s.HashAt(ctx, 1); return err }},
		{"SaveCheckpoint", func(ctx context.Context) error { return s.SaveCheckpoint(ctx, cp) }},
		{"Range", func(ctx context.Context) error { return s.Range(ctx, 1, func(audit.Entry) error { return nil }) }},
		{"Checkpoints", func(ctx context.Context) error { _, err := s.Checkpoints(ctx); return err }},
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

func TestReaderCannotAppend(t *testing.T) {
	c := pgtest.Cluster(t)
	if _, err := readerStore(t, c).Append(pgtest.Context(t), req(1)); pgCode(err) != "42501" {
		t.Fatalf("audit_reader append: err = %v, want 42501", err)
	}
}

// Even the owner cannot casually rewrite history; only disabling the trigger
// (a deliberate, detectable act) allows it.
func TestOwnerUpdateBlockedByTrigger(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := pgtest.Context(t)
	if _, err := appenderStore(t, c, 1).Append(ctx, req(1)); err != nil {
		t.Fatal(err)
	}
	owner := c.Owner(t, pg.StoreAudit)
	for _, q := range []string{"UPDATE audit.entries SET event_type = 'x.y.z'", "DELETE FROM audit.entries", "TRUNCATE audit.entries"} {
		if _, err := owner.Exec(ctx, q); pgCode(err) != "SA403" {
			t.Errorf("%q as owner: err = %v, want SA403", q, err)
		}
	}
}

func TestCheckpointsAreStoredAndAppendOnly(t *testing.T) {
	c := pgtest.Cluster(t)
	s := appenderStore(t, c, 1)
	ctx := pgtest.Context(t)
	cp := audit.Checkpoint{Origin: "sauti-test", Seq: 0, SignedAt: time.Now().UTC().Truncate(time.Microsecond), KeyID: "k1", Signature: []byte{1, 2, 3}}
	if err := s.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatal(err)
	}
	got, err := readerStore(t, c).Checkpoints(ctx)
	if err != nil || len(got) != 1 || got[0].KeyID != "k1" || got[0].Origin != "sauti-test" {
		t.Fatalf("Checkpoints = %+v, %v", got, err)
	}
	owner := c.Owner(t, pg.StoreAudit)
	if _, err := owner.Exec(ctx, "DELETE FROM audit.checkpoints"); pgCode(err) != "SA403" {
		t.Fatalf("checkpoint delete: %v", err)
	}
}

// CodeRabbit PR #3: timestamptz keeps microseconds, and Checkpoints rebuilds
// a checkpoint's body from the stored signed_at. A checkpoint signed over a
// finer SignedAt would be stored, but its rebuilt body would differ from the
// signed one and fail verification. SaveCheckpoint refuses it rather than
// truncate a checkpoint that is already signed.
func TestSaveCheckpointRefusesSubMicrosecondSignedAt(t *testing.T) {
	c := pgtest.Cluster(t)
	s, r := appenderStore(t, c, 1), readerStore(t, c)
	ctx := pgtest.Context(t)
	at := time.Date(2026, 11, 13, 9, 0, 0, 123456789, time.UTC)
	cp := audit.Checkpoint{Origin: "sauti-test", SignedAt: at, KeyID: "k1", Signature: []byte{1}}
	if err := s.SaveCheckpoint(ctx, cp); err == nil {
		t.Error("stored a checkpoint whose signed body PostgreSQL cannot keep")
	}
	if got, err := r.Checkpoints(ctx); err != nil || len(got) != 0 {
		t.Fatalf("after the refusal Checkpoints = %d stored, %v; want none", len(got), err)
	}
	cp.SignedAt = at.Truncate(time.Microsecond)
	if err := s.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatal(err)
	}
	got, err := r.Checkpoints(ctx)
	if err != nil || len(got) != 1 || string(got[0].Body()) != string(cp.Body()) {
		t.Fatalf("stored checkpoint does not rebuild the signed body (%d stored, %v)", len(got), err)
	}
}
