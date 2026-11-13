package audit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"sautihealth.com/sauti/internal/crypto"
)

// Checkpointer signs the current chain head with ML-DSA-65, stores the
// checkpoint in the ledger and writes it through the witness (spec §6.3).
//
// Anchoring (review finding C1): the checkpointer refuses to sign unless the
// ledger still holds the last witnessed head at that sequence. Otherwise a
// database owner could rewrite or truncate history, recompute the chain, and
// have the next checkpoint vouch for the forgery.
//
// Coordination (CodeRabbit PR #4): two kinds of writer sign checkpoints —
// the relay's leader, which keeps one Checkpointer for its leadership term,
// and `sautictl checkpoint` — and what they share is the write-once witness,
// not a lock. Before every signature, Checkpoint reads the witness again, so
// its anchor is the latest checkpoint any writer has witnessed, not only its
// own last one. Each note it has not seen before is signature-checked (fail
// closed) and checked against the chain, not only the latest: a note another
// writer witnessed after this one read the witness is caught by the next
// checkpoint even when a newer note exists.
//
// A Checkpointer is not safe for concurrent use.
type Checkpointer struct {
	ledger  Ledger
	signer  crypto.Signer
	witness WitnessStore
	origin  string
	now     func() time.Time
	anchor  *Checkpoint
	// seen holds the SHA-256 of every witness note already verified, so each
	// note is parsed, signature-checked and chain-checked once, not at every
	// checkpoint.
	seen map[[32]byte]struct{}
}

// WithAnchor sets the last trusted (witnessed) checkpoint. Checkpoint only
// ever moves the anchor forward, to a later witnessed checkpoint.
func (c *Checkpointer) WithAnchor(cp Checkpoint) *Checkpointer {
	c.anchor = &cp
	return c
}

// NewCheckpointer returns a Checkpointer. origin names the ledger, e.g.
// "sauti-dispatch/site-a".
func NewCheckpointer(l Ledger, s crypto.Signer, w WitnessStore, origin string, now func() time.Time) *Checkpointer {
	return &Checkpointer{ledger: l, signer: s, witness: w, origin: origin, now: now, seen: map[[32]byte]struct{}{}}
}

// Checkpoint signs and records the current head, after refreshing the
// anchor from the witness and checking that the ledger still holds it.
func (c *Checkpointer) Checkpoint(ctx context.Context) (Checkpoint, error) {
	seq, head, err := c.ledger.Head(ctx)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint: head: %w", err)
	}
	if err := c.refresh(ctx, seq); err != nil {
		return Checkpoint{}, err
	}
	if a := c.anchor; a != nil {
		if err := c.holds(ctx, seq, *a); err != nil {
			return Checkpoint{}, err
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
	note := cp.WitnessNote()
	if err := c.witness.Put(ctx, name, note); err != nil {
		return cp, fmt.Errorf("checkpoint: witness: %w", err)
	}
	c.seen[sha256.Sum256(note)] = struct{}{}
	c.anchor = &cp
	return cp, nil
}

// refresh reads the witness and verifies every note not seen before: its
// signature (a note that does not parse or verify is an error, fail closed)
// and that the ledger, whose head is at headSeq, still holds its head. The
// anchor moves to the latest note.
func (c *Checkpointer) refresh(ctx context.Context, headSeq int64) error {
	notes, err := c.witness.ReadAll()
	if err != nil {
		return fmt.Errorf("checkpoint: read witness: %w", err)
	}
	for i, n := range notes {
		d := sha256.Sum256(n)
		if _, ok := c.seen[d]; ok {
			continue
		}
		cp, err := verifiedNote(n, c.signer.PublicKey())
		if err != nil {
			return fmt.Errorf("checkpoint: witness note %d: %w", i, err)
		}
		if err := c.holds(ctx, headSeq, cp); err != nil {
			return err
		}
		c.seen[d] = struct{}{}
		if newer(cp, c.anchor) {
			c.anchor = &cp
		}
	}
	return nil
}

// holds returns ErrChainDiverged unless the ledger, whose head is at
// headSeq, still holds cp's head at cp's sequence.
func (c *Checkpointer) holds(ctx context.Context, headSeq int64, cp Checkpoint) error {
	if headSeq < cp.Seq {
		return fmt.Errorf("%w: head %d is behind witnessed seq %d (truncation)", ErrChainDiverged, headSeq, cp.Seq)
	}
	if cp.Seq == 0 {
		return nil
	}
	h, ok, err := c.ledger.HashAt(ctx, cp.Seq)
	if err != nil {
		return fmt.Errorf("checkpoint: hash at %d: %w", cp.Seq, err)
	}
	if !ok || h != cp.Head {
		return fmt.Errorf("%w: hash at witnessed seq %d changed (rewritten history)", ErrChainDiverged, cp.Seq)
	}
	return nil
}
