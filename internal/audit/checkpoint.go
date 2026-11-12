package audit

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"sautihealth.com/sauti/internal/crypto"
)

// Checkpointer signs the current chain head with ML-DSA-65, stores the
// checkpoint in the ledger and writes it through the witness (spec §6.3).
type Checkpointer struct {
	ledger  Ledger
	signer  crypto.Signer
	witness crypto.Witness
	origin  string
	now     func() time.Time
}

// NewCheckpointer returns a Checkpointer. origin names the ledger, e.g.
// "sauti-dispatch/site-a".
func NewCheckpointer(l Ledger, s crypto.Signer, w crypto.Witness, origin string, now func() time.Time) *Checkpointer {
	return &Checkpointer{ledger: l, signer: s, witness: w, origin: origin, now: now}
}

// Checkpoint signs and records the current head.
func (c *Checkpointer) Checkpoint(ctx context.Context) (Checkpoint, error) {
	seq, head, err := c.ledger.Head(ctx)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint: head: %w", err)
	}
	cp := Checkpoint{
		Origin: c.origin,
		Seq:    seq,
		Head:   head,
		// timestamptz keeps microseconds; truncate so the body re-derived
		// from the stored row is exactly what was signed.
		SignedAt: c.now().UTC().Truncate(time.Microsecond),
		KeyID:    c.signer.KeyID(),
	}
	sig, err := c.signer.Sign(ctx, cp.Body())
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint: sign: %w", err)
	}
	cp.Signature = sig
	if err := c.ledger.SaveCheckpoint(ctx, cp); err != nil {
		return Checkpoint{}, err
	}
	note := append(cp.Body(), []byte("— "+cp.KeyID+" "+base64.StdEncoding.EncodeToString(sig)+"\n")...)
	name := fmt.Sprintf("checkpoint-%d-%d.txt", cp.Seq, cp.SignedAt.UnixNano())
	if err := c.witness.Put(ctx, name, note); err != nil {
		return cp, fmt.Errorf("checkpoint: witness: %w", err)
	}
	return cp, nil
}
