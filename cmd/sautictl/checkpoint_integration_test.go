//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/platform/demo"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

// After a witnessed checkpoint, a database owner rewrites history and
// recomputes the chain. `sautictl checkpoint` must anchor on the witness and
// refuse, not sign and witness the forged head (CodeRabbit PR #4).
func TestCheckpointCommandRefusesARewrittenChain(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pg.Connect(ctx, c.DSN(pg.StoreAudit, "audit_appender"), "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ledger := auditstore.New(pool)
	appendEntries(ctx, t, ledger, 5)
	keys, witness := devKeys(t), t.TempDir()
	var out, errOut bytes.Buffer
	args := []string{"checkpoint", "--dsn", c.DSN(pg.StoreAudit, "audit_appender"), "--devkeys-dir", keys, "--witness-dir", witness}
	if code := Main(args, &out, &errOut); code != 0 {
		t.Fatalf("first checkpoint: exit=%d err=%s", code, errOut.String())
	}

	if _, err := c.Superuser(t, pg.StoreAudit).Exec(ctx, demo.RewriteAndRecomputeSQL); err != nil {
		t.Fatalf("attack: %v", err)
	}
	errOut.Reset()
	if code := Main(args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), audit.ErrChainDiverged.Error()) {
		t.Fatalf("checkpoint over rewritten history: exit=%d err=%s", code, errOut.String())
	}
	if notes, err := devkeys.NewFSWitness(witness).ReadAll(); err != nil || len(notes) != 1 {
		t.Fatalf("a checkpoint over forged history was witnessed (%d notes, %v)", len(notes), err)
	}
}

func appendEntries(ctx context.Context, t *testing.T, ledger *auditstore.Store, n int) {
	t.Helper()
	for i := range n {
		inc := uuid.NewV7()
		if _, err := ledger.Append(ctx, audit.AppendRequest{EntryID: uuid.NewV7(), OccurredAt: time.Now(),
			EventType: "screen.answered", IncidentID: &inc, PayloadSHA256: sha256.Sum256(fmt.Appendf(nil, "%d", i))}); err != nil {
			t.Fatal(err)
		}
	}
}

// CodeRabbit PR #4 (4563558512): the relay's leader keeps its checkpointer
// for the term while `sautictl checkpoint` writes to the same witness. The
// relay signs at seq 2; the command witnesses seq 7; an owner rewrites
// entry 3 and recomputes the chain, which leaves seq 2 intact. The relay's
// next checkpoint reads the witness again and refuses.
func TestRelayCheckpointRefusesARewriteBehindTheCommandsCheckpoint(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pg.Connect(ctx, c.DSN(pg.StoreAudit, "audit_appender"), "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ledger := auditstore.New(pool)
	keysDir, witnessDir := devKeys(t), t.TempDir()
	keys, err := devkeys.Open(keysDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	appendEntries(ctx, t, ledger, 2)
	relay, _, err := audit.NewAnchoredCheckpointer(ledger, keys.Signer(), devkeys.NewFSWitness(witnessDir), "sauti-dispatch", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if cp, err := relay.Checkpoint(ctx); err != nil || cp.Seq != 2 {
		t.Fatalf("relay checkpoint = %+v, %v; want seq 2", cp, err)
	}
	appendEntries(ctx, t, ledger, 5)
	var out, errOut bytes.Buffer
	args := []string{"checkpoint", "--dsn", c.DSN(pg.StoreAudit, "audit_appender"), "--devkeys-dir", keysDir, "--witness-dir", witnessDir}
	if code := Main(args, &out, &errOut); code != 0 || !strings.Contains(out.String(), "seq=7") {
		t.Fatalf("manual checkpoint: exit=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if _, err := c.Superuser(t, pg.StoreAudit).Exec(ctx, demo.RewriteAndRecomputeSQL); err != nil {
		t.Fatalf("attack: %v", err)
	}

	if _, err := relay.Checkpoint(ctx); !errors.Is(err, audit.ErrChainDiverged) {
		t.Fatalf("relay checkpoint over the rewrite: err = %v, want ErrChainDiverged", err)
	}
	if notes, err := devkeys.NewFSWitness(witnessDir).ReadAll(); err != nil || len(notes) != 2 {
		t.Fatalf("a checkpoint over rewritten history was witnessed (%d notes, %v)", len(notes), err)
	}
}
