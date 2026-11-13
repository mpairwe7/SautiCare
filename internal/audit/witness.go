package audit

import (
	"bytes"
	"crypto/sha256"
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
		cp, err := verifiedNote(n, pub)
		if err != nil {
			return nil, fmt.Errorf("witness note %d: %w", i, err)
		}
		if newer(cp, best) {
			best = &cp
		}
	}
	return best, nil
}

// verifiedNote parses a witness note and checks its signature against pub.
func verifiedNote(note, pub []byte) (Checkpoint, error) {
	cp, err := ParseWitnessNote(note)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := crypto.VerifyMLDSA65(pub, cp.Body(), cp.Signature); err != nil {
		return Checkpoint{}, fmt.Errorf("seq %d: %w", cp.Seq, err)
	}
	return cp, nil
}

// newer reports whether cp is later than best (nil is earliest): a higher
// sequence, or the same sequence signed later.
func newer(cp Checkpoint, best *Checkpoint) bool {
	return best == nil || cp.Seq > best.Seq || (cp.Seq == best.Seq && cp.SignedAt.After(best.SignedAt))
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
// The relay builds one per leadership term and the CLI one per run, so a
// corrupt witness stops them before they lead or sign. Each Checkpoint then
// reads the witness again: notes present now are not chain-checked one by
// one (the latest is, at every Checkpoint), while every note written later,
// by any writer, is checked when Checkpoint first sees it (CodeRabbit PR #4).
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
	for _, n := range notes {
		cp.seen[sha256.Sum256(n)] = struct{}{}
	}
	if anchor != nil {
		cp.WithAnchor(*anchor)
	}
	return cp, anchor, nil
}
