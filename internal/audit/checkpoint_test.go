package audit

import (
	"bytes"
	"context"
	"crypto/mldsa"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/crypto"
)

type testSigner struct{ sk *mldsa.PrivateKey }

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	return &testSigner{sk: sk}
}

func (s *testSigner) KeyID() string     { return "test:1" }
func (s *testSigner) PublicKey() []byte { return s.sk.PublicKey().Bytes() }
func (s *testSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return crypto.SignMLDSA65(s.sk, msg)
}

type memWitness struct{ files map[string][]byte }

func (w *memWitness) Put(_ context.Context, name string, data []byte) error {
	if w.files == nil {
		w.files = map[string][]byte{}
	}
	w.files[name] = data
	return nil
}

func (w *memWitness) ReadAll() ([][]byte, error) {
	names := slices.Sorted(maps.Keys(w.files))
	out := make([][]byte, 0, len(names))
	for _, n := range names {
		out = append(out, w.files[n])
	}
	return out, nil
}

func TestCheckpointSignedAndWitnessed(t *testing.T) {
	l := newFakeLedger()
	ctx := context.Background()
	for i := range 7 {
		req, _ := ParseOutboxPayload(outboxRow(i))
		if _, err := l.Append(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	signer := newTestSigner(t)
	w := &memWitness{}
	now := time.Date(2026, 11, 12, 10, 5, 0, 123456789, time.UTC)
	cp, err := NewCheckpointer(l, signer, w, "sauti-test", func() time.Time { return now }).Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seq, head, _ := l.Head(ctx)
	if cp.Seq != seq || cp.Head != head || cp.KeyID != "test:1" {
		t.Fatalf("checkpoint = %+v", cp)
	}
	if cp.SignedAt != now.Truncate(time.Microsecond) {
		t.Fatalf("SignedAt %v not truncated to microseconds", cp.SignedAt)
	}
	if err := crypto.VerifyMLDSA65(signer.PublicKey(), cp.Body(), cp.Signature); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	if len(l.checkpoints) != 1 {
		t.Fatalf("ledger checkpoints = %d", len(l.checkpoints))
	}
	if len(w.files) != 1 {
		t.Fatalf("witness files = %d", len(w.files))
	}
	for name, data := range w.files {
		if !strings.HasPrefix(name, "checkpoint-7-") || !bytes.HasPrefix(data, cp.Body()) {
			t.Fatalf("witness %s = %q", name, data)
		}
	}
	if !strings.HasPrefix(string(cp.Body()), CheckpointFormat+"\nsauti-test\n7\n") {
		t.Fatalf("body = %q", cp.Body())
	}
}

// The signed_at line of the checkpoint body is normative
// (docs/reference/audit-canonical-encoding.md): UTC with a "Z" suffix, one
// to six fractional digits with trailing zeros removed, and no fraction for
// a whole second. An implementation that writes ".123456000Z" signs a
// different body (CodeRabbit PR #5).
func TestCheckpointBodySignedAtFormat(t *testing.T) {
	kampala := time.FixedZone("EAT", 3*60*60)
	for want, at := range map[string]time.Time{
		"2026-11-12T08:30:00Z":        time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC),
		"2026-11-12T08:30:00.123456Z": time.Date(2026, 11, 12, 8, 30, 0, 123456000, time.UTC),
		"2026-11-12T08:30:00.12Z":     time.Date(2026, 11, 12, 8, 30, 0, 120000000, time.UTC),
		"2026-11-12T05:30:00.000001Z": time.Date(2026, 11, 12, 8, 30, 0, 1000, kampala),
	} {
		body := string(Checkpoint{Origin: "o", Seq: 1, SignedAt: at}.Body())
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		if got := lines[len(lines)-1]; got != want {
			t.Errorf("signed_at line = %q, want %q", got, want)
		}
	}
}
