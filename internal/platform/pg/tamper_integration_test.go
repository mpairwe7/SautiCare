//go:build integration

package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/demo"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
)

func TestOwnerRewriteWithRecomputedChainIsCaughtByWitness(t *testing.T) {
	c, e := newEnv(t)
	// Bounded: the superuser Exec below bypasses the store adapters' own
	// deadlines (CodeRabbit PR #4).
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	phone, _ := pii.ParsePhone("0772123456")
	o, err := e.svc.StartSession(ctx, incident.StartSession{Channel: incident.ChannelUSSDAT,
		ProviderSessionID: "ATUid_tamper", Phone: phone, IdemKey: "start"})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := e.svc.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: incident.EvScreenAnswered,
			Payload: json.RawMessage(`{"screen":2,"field":"danger","value":"no"}`), ActorKind: incident.ActorCaller, IdemKey: uuid.NewV4().String()}); err != nil {
			t.Fatal(err)
		}
	}
	drain(t, e.relay)
	witnessDir := t.TempDir()
	witness := devkeys.NewFSWitness(witnessDir)
	first, err := audit.NewCheckpointer(e.ledger, e.keys.Signer(), witness, "sauti-test", time.Now).Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Superuser(t, pg.StoreAudit).Exec(ctx, demo.RewriteAndRecomputeSQL); err != nil {
		t.Fatalf("attack: %v", err)
	}
	pub := e.keys.Signer().PublicKey()

	// Database-only verification is fooled — which is why witnesses are
	// required. A read error leaves a zero report that OK() accepts, so it
	// must not pass for a completed verification (CodeRabbit PR #4).
	if rep, err := audit.Verify(ctx, e.reader, pub, nil); err != nil || !rep.OK() {
		t.Fatalf("precondition: database-only verification should complete and be fooled: %+v, %v", rep, err)
	}

	notes, err := witness.ReadAll()
	if err != nil || len(notes) != 1 {
		t.Fatalf("witness notes = %d, %v", len(notes), err)
	}
	wcp, err := audit.ParseWitnessNote(notes[0])
	if err != nil {
		t.Fatal(err)
	}
	rep, err := audit.Verify(ctx, e.reader, pub, []audit.Checkpoint{wcp})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || rep.WitnessesBad != 1 {
		t.Fatalf("witness did not catch the rewrite: %+v", rep)
	}

	// The relay's checkpointer, anchored from the witness, refuses to sign.
	_, err = audit.NewCheckpointer(e.ledger, e.keys.Signer(), witness, "sauti-test", time.Now).WithAnchor(first).Checkpoint(ctx)
	if !errors.Is(err, audit.ErrChainDiverged) {
		t.Fatalf("checkpointer over forged history: err = %v, want ErrChainDiverged", err)
	}
	if again, _ := witness.ReadAll(); len(again) != 1 {
		t.Fatalf("a checkpoint over forged history was witnessed (%d notes)", len(again))
	}
}
