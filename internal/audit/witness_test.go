package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func witnessOf(t *testing.T, w *memWitness) []Checkpoint {
	t.Helper()
	var out []Checkpoint
	for _, note := range w.files {
		cp, err := ParseWitnessNote(note)
		if err != nil {
			t.Fatalf("ParseWitnessNote: %v", err)
		}
		out = append(out, cp)
	}
	return out
}

// rewriteFrom simulates a database owner who rewrites entry seq and then
// recomputes every later hash, so the chain alone looks intact.
func rewriteFrom(l *fakeLedger, seq int64, eventType string) {
	l.entries[seq-1].EventType = eventType
	var prev [32]byte
	if seq > 1 {
		prev = l.entries[seq-2].Hash
	}
	for i := seq - 1; i < int64(len(l.entries)); i++ {
		l.entries[i].PrevHash = prev
		l.entries[i].Hash = ChainHash(prev, l.entries[i])
		prev = l.entries[i].Hash
	}
}

func appendN(t *testing.T, l *fakeLedger, from, n int) {
	t.Helper()
	for i := range n {
		req, _ := ParseOutboxPayload(outboxRow(from + i))
		if _, err := l.Append(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseWitnessNoteRoundTrip(t *testing.T) {
	l := newFakeLedger()
	appendN(t, l, 0, 3)
	w := &memWitness{}
	cp, err := NewCheckpointer(l, newTestSigner(t), w, "sauti-test", time.Now).Checkpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := witnessOf(t, w)
	if len(got) != 1 || got[0].Seq != cp.Seq || got[0].Head != cp.Head || !got[0].SignedAt.Equal(cp.SignedAt) ||
		got[0].KeyID != cp.KeyID || string(got[0].Signature) != string(cp.Signature) || got[0].Origin != cp.Origin {
		t.Fatalf("parsed %+v, want %+v", got, cp)
	}
}

func TestParseWitnessNoteRejectsGarbage(t *testing.T) {
	for _, note := range []string{"", "hello", CheckpointFormat + "\no\nx\n\n\n", CheckpointFormat + "\no\n1\nAAAA\n2026-11-12T00:00:00Z\n"} {
		if _, err := ParseWitnessNote([]byte(note)); err == nil {
			t.Errorf("accepted %q", note)
		}
	}
}

// Review finding C1: rewriting an entry, recomputing the chain and deleting
// the database checkpoints defeats database-only verification; the
// write-once witness must catch it.
func TestVerifyWithWitnessDetectsRewrittenChain(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	appendN(t, l, 0, 10)
	signer := newTestSigner(t)
	w := &memWitness{}
	if _, err := NewCheckpointer(l, signer, w, "sauti-test", time.Now).Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	rewriteFrom(l, 3, "incident.closed")
	l.checkpoints = nil // attacker deletes the database checkpoints

	if rep, _ := Verify(ctx, l, signer.PublicKey(), nil); !rep.OK() {
		t.Fatalf("precondition: database-only verification should be fooled, got %+v", rep)
	}
	rep, err := Verify(ctx, l, signer.PublicKey(), witnessOf(t, w))
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || rep.WitnessesBad == 0 {
		t.Fatalf("rewritten chain not detected by witness: %+v", rep)
	}
}

func TestVerifyWithWitnessDetectsTruncation(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	appendN(t, l, 0, 10)
	signer := newTestSigner(t)
	w := &memWitness{}
	if _, err := NewCheckpointer(l, signer, w, "sauti-test", time.Now).Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	l.entries = l.entries[:7]
	l.checkpoints = nil
	rep, _ := Verify(ctx, l, signer.PublicKey(), witnessOf(t, w))
	if rep.OK() || rep.WitnessesBad != 1 {
		t.Fatalf("truncation not detected by witness: %+v", rep)
	}
}

func TestVerifyAcceptsMatchingWitnesses(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	appendN(t, l, 0, 5)
	signer := newTestSigner(t)
	w := &memWitness{}
	cp := NewCheckpointer(l, signer, w, "sauti-test", time.Now)
	if _, err := cp.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	appendN(t, l, 5, 5)
	if _, err := cp.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	rep, _ := Verify(ctx, l, signer.PublicKey(), witnessOf(t, w))
	if !rep.OK() || rep.WitnessesOK != 2 {
		t.Fatalf("intact ledger with witnesses: %+v", rep)
	}
}

// The checkpointer must never sign a head whose history no longer contains
// the last witnessed checkpoint.
func TestCheckpointerRefusesDivergedChain(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	appendN(t, l, 0, 6)
	signer := newTestSigner(t)
	w := &memWitness{}
	first, err := NewCheckpointer(l, signer, w, "sauti-test", time.Now).Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rewriteFrom(l, 2, "incident.closed")
	cp := NewCheckpointer(l, signer, w, "sauti-test", time.Now).WithAnchor(first)
	if _, err := cp.Checkpoint(ctx); !errors.Is(err, ErrChainDiverged) {
		t.Fatalf("err = %v, want ErrChainDiverged", err)
	}
	if len(l.checkpoints) != 1 || len(w.files) != 1 {
		t.Fatalf("a checkpoint was signed over forged history (db=%d witness=%d)", len(l.checkpoints), len(w.files))
	}
}

func TestCheckpointerRefusesTruncatedChain(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	appendN(t, l, 0, 6)
	signer := newTestSigner(t)
	first, _ := NewCheckpointer(l, signer, &memWitness{}, "sauti-test", time.Now).Checkpoint(ctx)
	l.entries = l.entries[:4]
	if _, err := NewCheckpointer(l, signer, &memWitness{}, "sauti-test", time.Now).WithAnchor(first).Checkpoint(ctx); !errors.Is(err, ErrChainDiverged) {
		t.Fatalf("err = %v, want ErrChainDiverged", err)
	}
}

func TestCheckpointerAdvancesItsAnchor(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	appendN(t, l, 0, 3)
	cp := NewCheckpointer(l, newTestSigner(t), &memWitness{}, "sauti-test", time.Now)
	for i := range 3 {
		appendN(t, l, 10*(i+1), 2)
		if _, err := cp.Checkpoint(ctx); err != nil {
			t.Fatalf("checkpoint %d on an intact chain: %v", i, err)
		}
	}
	// The anchor has advanced to seq 9: rewriting seq 6 (after the first
	// checkpoint at seq 5, before the latest) must now be refused
	// (CodeRabbit PR #2).
	rewriteFrom(l, 6, "incident.closed")
	if _, err := cp.Checkpoint(ctx); !errors.Is(err, ErrChainDiverged) {
		t.Fatalf("rewrite behind the advanced anchor: err = %v, want ErrChainDiverged", err)
	}
}

func TestLatestWitnessedPicksHighestValidAndFailsClosed(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	signer := newTestSigner(t)
	w := &memWitness{}
	cp := NewCheckpointer(l, signer, w, "sauti-test", time.Now)
	appendN(t, l, 0, 2)
	if _, err := cp.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	appendN(t, l, 2, 3)
	second, err := cp.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var notes [][]byte
	for _, n := range w.files {
		notes = append(notes, n)
	}
	got, err := LatestWitnessed(notes, signer.PublicKey())
	if err != nil || got == nil || got.Seq != second.Seq || got.Head != second.Head {
		t.Fatalf("LatestWitnessed = %+v, %v; want seq %d", got, err, second.Seq)
	}
	if none, err := LatestWitnessed(nil, signer.PublicKey()); err != nil || none != nil {
		t.Fatalf("no notes: %+v, %v", none, err)
	}
	forged := append([][]byte{}, notes...)
	forged = append(forged, []byte(string(notes[0])[:len(notes[0])-5]+"AAAA\n"))
	if _, err := LatestWitnessed(forged, signer.PublicKey()); err == nil {
		t.Fatal("a corrupt witness note was ignored instead of failing closed")
	}
	other := newTestSigner(t)
	if _, err := LatestWitnessed(notes, other.PublicKey()); err == nil {
		t.Fatal("notes signed by another key were accepted")
	}
}

// A standby that takes over must anchor on what the previous leader
// witnessed while it waited, not on what it read at start-up (CodeRabbit
// PR #4): with the older anchor it would sign a rewrite made after it.
func TestAnchoredCheckpointerUsesTheLatestWitnessEachTerm(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	signer := newTestSigner(t)
	w := &memWitness{}
	appendN(t, l, 0, 3)
	if _, err := NewCheckpointer(l, signer, w, "sauti-test", time.Now).Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	// The standby's start-up read.
	stale, startAnchor, err := NewAnchoredCheckpointer(l, signer, w, "sauti-test", time.Now)
	if err != nil || startAnchor == nil || startAnchor.Seq != 3 {
		t.Fatalf("start-up anchor = %+v, %v; want seq 3", startAnchor, err)
	}
	// Meanwhile the active leader witnesses seq 7; then an attacker rewrites
	// entry 5, between the two anchors, and recomputes the chain.
	appendN(t, l, 3, 4)
	newer, err := NewCheckpointer(l, signer, w, "sauti-test", time.Now).WithAnchor(*startAnchor).Checkpoint(ctx)
	if err != nil || newer.Seq != 7 {
		t.Fatalf("leader checkpoint = %+v, %v", newer, err)
	}
	rewriteFrom(l, 5, "incident.closed")

	// The next term reads the witness again: anchored on seq 7, it refuses.
	term, anchor, err := NewAnchoredCheckpointer(l, signer, w, "sauti-test", time.Now)
	if err != nil || anchor == nil || anchor.Seq != newer.Seq || anchor.Head != newer.Head {
		t.Fatalf("next-term anchor = %+v, %v; want seq %d", anchor, err, newer.Seq)
	}
	if _, err := term.Checkpoint(ctx); !errors.Is(err, ErrChainDiverged) {
		t.Fatalf("next term over the rewrite: err = %v, want ErrChainDiverged", err)
	}
	// What the fix prevents: the start-up checkpointer still signs.
	if _, err := stale.Checkpoint(ctx); err != nil {
		t.Fatalf("precondition: the stale anchor should not see the rewrite: %v", err)
	}
}

func TestAnchoredCheckpointerEmptyWitnessAndFailClosed(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	signer := newTestSigner(t)
	w := &memWitness{}
	appendN(t, l, 0, 2)
	cp, anchor, err := NewAnchoredCheckpointer(l, signer, w, "sauti-test", time.Now)
	if err != nil || anchor != nil {
		t.Fatalf("empty witness: anchor = %+v, %v; want none", anchor, err)
	}
	if _, err := cp.Checkpoint(ctx); err != nil {
		t.Fatalf("unanchored first checkpoint: %v", err)
	}
	if _, _, err := NewAnchoredCheckpointer(l, newTestSigner(t), w, "sauti-test", time.Now); err == nil {
		t.Fatal("a note signed by another key was accepted")
	}
	w.files["checkpoint-9-1.txt"] = []byte("not a note")
	if _, _, err := NewAnchoredCheckpointer(l, signer, w, "sauti-test", time.Now); err == nil {
		t.Fatal("a corrupt witness note was ignored instead of failing closed")
	}
}
