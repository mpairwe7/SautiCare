package audit

import (
	"context"
	"testing"
	"time"
)

func ledgerWith(t *testing.T, n int) (*fakeLedger, *testSigner) {
	t.Helper()
	l := newFakeLedger()
	ctx := context.Background()
	signer := newTestSigner(t)
	cp := NewCheckpointer(l, signer, &memWitness{}, "sauti-test", time.Now)
	for i := range n {
		req, _ := ParseOutboxPayload(outboxRow(i))
		if _, err := l.Append(ctx, req); err != nil {
			t.Fatal(err)
		}
		if i == n/2 || i == n-1 {
			if _, err := cp.Checkpoint(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	return l, signer
}

func TestVerifyAcceptsValidChain(t *testing.T) {
	l, s := ledgerWith(t, 10)
	rep, err := Verify(context.Background(), l, s.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Entries != 10 || rep.HeadSeq != 10 || rep.CheckpointsOK != 2 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestVerifyDetectsBrokenHash(t *testing.T) {
	l, s := ledgerWith(t, 10)
	l.entries[2].EventType = "incident.closed" // rewritten history at seq 3
	rep, err := Verify(context.Background(), l, s.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || rep.FirstBadSeq != 3 {
		t.Fatalf("report = %+v, want FirstBadSeq 3", rep)
	}
}

func TestVerifyDetectsSeqGap(t *testing.T) {
	l, s := ledgerWith(t, 10)
	l.entries = append(l.entries[:2], l.entries[3:]...) // seq 3 deleted
	rep, _ := Verify(context.Background(), l, s.PublicKey())
	if rep.OK() || rep.FirstBadSeq != 3 {
		t.Fatalf("report = %+v, want FirstBadSeq 3", rep)
	}
}

// Truncating the tail removes entries a checkpoint already vouched for.
func TestVerifyDetectsTruncationBeyondCheckpoint(t *testing.T) {
	l, s := ledgerWith(t, 10)
	l.entries = l.entries[:8]
	rep, _ := Verify(context.Background(), l, s.PublicKey())
	if rep.OK() || rep.CheckpointsBad == 0 {
		t.Fatalf("truncation not detected: %+v", rep)
	}
}

func TestVerifyDetectsForgedCheckpoint(t *testing.T) {
	l, s := ledgerWith(t, 10)
	other := newTestSigner(t)
	forged := l.checkpoints[1]
	forged.Signature, _ = other.Sign(context.Background(), forged.Body())
	l.checkpoints[1] = forged
	rep, _ := Verify(context.Background(), l, s.PublicKey())
	if rep.OK() || rep.CheckpointsBad != 1 || rep.CheckpointsOK != 1 {
		t.Fatalf("forged checkpoint not detected: %+v", rep)
	}
}
