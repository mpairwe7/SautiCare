package audit

import (
	"context"
	"fmt"
	"time"

	"sautihealth.com/sauti/internal/crypto"
)

// Checkpointer signs the current chain head with ML-DSA-65, stores the
// checkpoint in the ledger and writes it through the witness (spec §6.3).
//
// Anchoring (review finding C1): the checkpointer remembers the last
// checkpoint it signed — seeded at start-up from the write-once witness —
// and refuses to sign unless the ledger still holds that head at that
// sequence. Otherwise a database owner could rewrite or truncate history,
// recompute the chain, and have the next checkpoint vouch for the forgery.
type Checkpointer struct {
	ledger  Ledger
	signer  crypto.Signer
	witness crypto.Witness
	origin  string
	now     func() time.Time
	anchor  *Checkpoint
}

// WithAnchor sets the last trusted (witnessed) checkpoint.
func (c *Checkpointer) WithAnchor(cp Checkpoint) *Checkpointer {
	c.anchor = &cp
	return c
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
	if a := c.anchor; a != nil {
		if seq < a.Seq {
			return Checkpoint{}, fmt.Errorf("%w: head %d is behind witnessed seq %d (truncation)", ErrChainDiverged, seq, a.Seq)
		}
		if a.Seq > 0 {
			h, ok, err := c.ledger.HashAt(ctx, a.Seq)
			if err != nil {
				return Checkpoint{}, fmt.Errorf("checkpoint: hash at %d: %w", a.Seq, err)
			}
			if !ok || h != a.Head {
				return Checkpoint{}, fmt.Errorf("%w: hash at witnessed seq %d changed (rewritten history)", ErrChainDiverged, a.Seq)
			}
		}
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
	name := fmt.Sprintf("checkpoint-%d-%d.txt", cp.Seq, cp.SignedAt.UnixNano())
	if err := c.witness.Put(ctx, name, cp.WitnessNote()); err != nil {
		return cp, fmt.Errorf("checkpoint: witness: %w", err)
	}
	c.anchor = &cp
	return cp, nil
}
