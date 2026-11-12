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
	WitnessesOK    int
	WitnessesBad   int
	FirstBadSeq    int64  // 0 when the chain is intact
	Problem        string // first problem found, for humans
}

// OK reports whether the chain, every checkpoint and every witness verified.
func (r VerifyReport) OK() bool {
	return r.FirstBadSeq == 0 && r.CheckpointsBad == 0 && r.WitnessesBad == 0
}

func (r *VerifyReport) note(problem string) {
	if r.Problem == "" {
		r.Problem = problem
	}
}

var errStop = errors.New("stop")

// Verify recomputes every hash from the canonical encoding, checks sequence
// continuity and links, and checks every checkpoint's signature and that its
// head matches the chain at its sequence. A checkpoint beyond the current
// head reveals truncation.
//
// witnesses are the checkpoints read from the write-once witness store
// (outside the database). They are what defeats a database owner who
// rewrites history, recomputes the chain and deletes the checkpoints in the
// database (review finding C1): each witness must carry a valid signature,
// lie within the chain, match the chain hash at its sequence, and still be
// present among the database checkpoints. Only I/O failures return an error.
func Verify(ctx context.Context, r LedgerReader, pub []byte, witnesses []Checkpoint) (VerifyReport, error) {
	var rep VerifyReport
	cps, err := r.Checkpoints(ctx)
	if err != nil {
		return rep, err
	}
	need := map[int64]bool{}
	for _, c := range cps {
		need[c.Seq] = true
	}
	for _, w := range witnesses {
		need[w.Seq] = true
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
			rep.note(problem)
			continue
		}
		rep.CheckpointsOK++
	}

	inDB := map[string]bool{}
	for _, c := range cps {
		inDB[string(c.Body())] = true
	}
	for _, w := range witnesses {
		problem := ""
		switch h, ok := hashAt[w.Seq]; {
		case crypto.VerifyMLDSA65(pub, w.Body(), w.Signature) != nil:
			problem = fmt.Sprintf("witness at seq %d: signature invalid", w.Seq)
		case !ok:
			problem = fmt.Sprintf("witness at seq %d: beyond the verified chain (head %d) — truncated or broken history", w.Seq, rep.HeadSeq)
		case h != w.Head:
			problem = fmt.Sprintf("witness at seq %d: chain hash differs — history rewritten", w.Seq)
		case !inDB[string(w.Body())]:
			problem = fmt.Sprintf("witness at seq %d: checkpoint missing from the ledger — deleted", w.Seq)
		}
		if problem != "" {
			rep.WitnessesBad++
			rep.note(problem)
			continue
		}
		rep.WitnessesOK++
	}
	return rep, nil
}
