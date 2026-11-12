package audit

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sautihealth.com/sauti/internal/crypto"
)

// ErrChainDiverged means the ledger no longer contains the last witnessed
// checkpoint's head at its sequence (history rewritten or truncated). The
// checkpointer refuses to sign over it (review finding C1).
var ErrChainDiverged = errors.New("audit: ledger diverged from the last witnessed checkpoint")

// witnessSigPrefix starts the signature line of a witness note.
const witnessSigPrefix = "— "

// WitnessNote is the exact content written to the witness: the signed body
// followed by "— <key id> <base64 signature>\n".
func (c Checkpoint) WitnessNote() []byte {
	return append(c.Body(), []byte(witnessSigPrefix+c.KeyID+" "+base64.StdEncoding.EncodeToString(c.Signature)+"\n")...)
}

// ParseWitnessNote reads a witness note back into a checkpoint. It checks
// the format only; the signature is verified by Verify or the caller.
func ParseWitnessNote(note []byte) (Checkpoint, error) {
	bad := func(why string) (Checkpoint, error) {
		return Checkpoint{}, fmt.Errorf("audit: witness note: %s", why)
	}
	lines := strings.Split(strings.TrimSuffix(string(note), "\n"), "\n")
	if len(lines) != 6 || lines[0] != CheckpointFormat {
		return bad("not a " + CheckpointFormat + " note")
	}
	seq, err := strconv.ParseInt(lines[2], 10, 64)
	if err != nil || seq < 0 {
		return bad("sequence")
	}
	head, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(head) != 32 {
		return bad("head")
	}
	at, err := time.Parse(time.RFC3339Nano, lines[4])
	if err != nil {
		return bad("time")
	}
	sigLine, ok := strings.CutPrefix(lines[5], witnessSigPrefix)
	if !ok {
		return bad("signature line")
	}
	keyID, sigB64, ok := strings.Cut(sigLine, " ")
	if !ok || keyID == "" {
		return bad("signature line")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) == 0 {
		return bad("signature")
	}
	cp := Checkpoint{Origin: lines[1], Seq: seq, SignedAt: at.UTC(), KeyID: keyID, Signature: sig}
	copy(cp.Head[:], head)
	if !bytes.Equal(cp.WitnessNote(), note) {
		return bad("not in canonical form")
	}
	return cp, nil
}

// LatestWitnessed parses and signature-checks every witness note and returns
// the checkpoint with the highest sequence, or nil if there are none. Any
// unparseable or wrongly signed note is an error: a tampered witness store
// must stop the relay rather than be silently skipped (fail closed).
func LatestWitnessed(notes [][]byte, pub []byte) (*Checkpoint, error) {
	var best *Checkpoint
	for i, n := range notes {
		cp, err := ParseWitnessNote(n)
		if err != nil {
			return nil, fmt.Errorf("witness note %d: %w", i, err)
		}
		if err := crypto.VerifyMLDSA65(pub, cp.Body(), cp.Signature); err != nil {
			return nil, fmt.Errorf("witness note %d (seq %d): %w", i, cp.Seq, err)
		}
		if best == nil || cp.Seq > best.Seq || (cp.Seq == best.Seq && cp.SignedAt.After(best.SignedAt)) {
			c := cp
			best = &c
		}
	}
	return best, nil
}

// WitnessStore is a witness that can also read back every note it holds.
type WitnessStore interface {
	crypto.Witness
	ReadAll() ([][]byte, error)
}

// NewAnchoredCheckpointer reads and signature-checks every note in w and
// returns a Checkpointer anchored on the latest (unanchored only when w
// holds none), together with that anchor. A note that does not parse or
// verify is an error (fail closed, as LatestWitnessed).
//
// Build one immediately before signing — per leadership term, per CLI run —
// never once per process: another instance may have witnessed newer history
// meanwhile, and an older anchor would let a rewrite made after it be
// signed (CodeRabbit PR #4).
func NewAnchoredCheckpointer(l Ledger, s crypto.Signer, w WitnessStore, origin string, now func() time.Time) (*Checkpointer, *Checkpoint, error) {
	notes, err := w.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	anchor, err := LatestWitnessed(notes, s.PublicKey())
	if err != nil {
		return nil, nil, err
	}
	cp := NewCheckpointer(l, s, w, origin, now)
	if anchor != nil {
		cp.WithAnchor(*anchor)
	}
	return cp, anchor, nil
}
