package audit

import (
	"context"
	"errors"
	"fmt"

	"sautihealth.com/sauti/internal/crypto"
)

// VerifyReport summarises a full verification of the ledger.
type VerifyReport struct {
	Entries        int64
	HeadSeq        int64
	Head           [32]byte
	CheckpointsOK  int
	CheckpointsBad int
	FirstBadSeq    int64  // 0 when the chain is intact
	Problem        string // first problem found, for humans
}

// OK reports whether the chain and every checkpoint verified.
func (r VerifyReport) OK() bool { return r.FirstBadSeq == 0 && r.CheckpointsBad == 0 }

var errStop = errors.New("stop")

// Verify recomputes every hash from the canonical encoding, checks sequence
// continuity and links, and checks every checkpoint's signature and that its
// head matches the chain at its sequence. A checkpoint beyond the current
// head reveals truncation. Only I/O failures return an error.
func Verify(ctx context.Context, r LedgerReader, pub []byte) (VerifyReport, error) {
	var rep VerifyReport
	cps, err := r.Checkpoints(ctx)
	if err != nil {
		return rep, err
	}
	need := map[int64]bool{}
	for _, c := range cps {
		need[c.Seq] = true
	}
	hashAt := map[int64][32]byte{0: {}}

	var prev [32]byte
	expect := int64(1)
	err = r.Range(ctx, 1, func(e Entry) error {
		switch {
		case e.Seq != expect:
			rep.FirstBadSeq, rep.Problem = expect, fmt.Sprintf("sequence gap: expected %d, found %d", expect, e.Seq)
		case e.PrevHash != prev:
			rep.FirstBadSeq, rep.Problem = e.Seq, "prev_hash does not link to the previous entry"
		case ChainHash(prev, e) != e.Hash:
			rep.FirstBadSeq, rep.Problem = e.Seq, "hash does not match the canonical encoding"
		}
		if rep.FirstBadSeq != 0 {
			return errStop
		}
		rep.Entries++
		rep.HeadSeq, rep.Head = e.Seq, e.Hash
		if need[e.Seq] {
			hashAt[e.Seq] = e.Hash
		}
		prev = e.Hash
		expect++
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return rep, err
	}

	for _, c := range cps {
		problem := ""
		switch h, ok := hashAt[c.Seq]; {
		case crypto.VerifyMLDSA65(pub, c.Body(), c.Signature) != nil:
			problem = fmt.Sprintf("checkpoint at seq %d: signature invalid", c.Seq)
		case !ok:
			problem = fmt.Sprintf("checkpoint at seq %d: beyond the verified chain (head %d) — truncation or break", c.Seq, rep.HeadSeq)
		case h != c.Head:
			problem = fmt.Sprintf("checkpoint at seq %d: head differs from the chain", c.Seq)
		}
		if problem != "" {
			rep.CheckpointsBad++
			if rep.Problem == "" {
				rep.Problem = problem
			}
			continue
		}
		rep.CheckpointsOK++
	}
	return rep, nil
}
