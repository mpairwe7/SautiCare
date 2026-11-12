//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	for i := range 5 {
		inc := uuid.NewV7()
		if _, err := ledger.Append(ctx, audit.AppendRequest{EntryID: uuid.NewV7(), OccurredAt: time.Now(),
			EventType: "screen.answered", IncidentID: &inc, PayloadSHA256: sha256.Sum256(fmt.Appendf(nil, "%d", i))}); err != nil {
			t.Fatal(err)
		}
	}
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
