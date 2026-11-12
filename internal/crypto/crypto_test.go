package crypto

import (
	"bytes"
	"context"
	"crypto/mldsa"
	"crypto/rand"
	"errors"
	"testing"

	"sautihealth.com/sauti/internal/pii"
)

// memKeys is a minimal in-memory KeyProvider for unit tests.
type memKeys struct {
	cur, prev DataKey
	hasPrev   bool
}

func newMemKeys(t *testing.T) *memKeys {
	t.Helper()
	k := func(id string) DataKey {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		return DataKey{ID: id, Key: b}
	}
	return &memKeys{cur: k("p2"), prev: k("p1"), hasPrev: true}
}

func (m *memKeys) Current(context.Context, string) (DataKey, error) { return m.cur, nil }
func (m *memKeys) Previous(context.Context, string) (DataKey, bool, error) {
	return m.prev, m.hasPrev, nil
}
func (m *memKeys) ByID(_ context.Context, id string) (DataKey, error) {
	switch id {
	case m.cur.ID:
		return m.cur, nil
	case m.prev.ID:
		return m.prev, nil
	}
	return DataKey{}, ErrUnknownKey
}

var aad = AAD{Store: "operational", Table: "sessions", Column: "phone", RowID: "0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70"}

func TestSealOpenRoundTrip(t *testing.T) {
	env := NewEnvelope(newMemKeys(t), rand.Reader)
	ctx := context.Background()
	s, err := env.Seal(ctx, ClassIdentifiers, []byte("+256772123456"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyID != "p2" {
		t.Fatalf("sealed with %q, want current key p2", s.KeyID)
	}
	if bytes.Contains(s.Ciphertext, []byte("772123456")) {
		t.Fatal("ciphertext contains plaintext")
	}
	pt, err := env.Open(ctx, s, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "+256772123456" {
		t.Fatalf("Open = %q", pt)
	}
}

// The AAD binds a ciphertext to its row: copying a number's ciphertext onto
// another session row must fail to decrypt rather than silently move it.
func TestOpenFailsWithSwappedAAD(t *testing.T) {
	env := NewEnvelope(newMemKeys(t), rand.Reader)
	ctx := context.Background()
	s, _ := env.Seal(ctx, ClassIdentifiers, []byte("x"), aad)
	other := aad
	other.RowID = "0195f3a2-7c1e-7d4b-9a10-000000000000"
	if _, err := env.Open(ctx, s, other); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Open with swapped row = %v, want ErrDecrypt", err)
	}
}

func TestOpenFailsWithUnknownKeyID(t *testing.T) {
	env := NewEnvelope(newMemKeys(t), rand.Reader)
	ctx := context.Background()
	s, _ := env.Seal(ctx, ClassIdentifiers, []byte("x"), aad)
	s.KeyID = "destroyed"
	if _, err := env.Open(ctx, s, aad); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Open with unknown key = %v, want ErrUnknownKey", err)
	}
}

// AAD encoding must be unambiguous: moving a separator between fields must
// change the bytes (length-prefixed, not delimiter-joined).
func TestAADBytesAreUnambiguous(t *testing.T) {
	a := AAD{Store: "ab", Table: "c"}
	b := AAD{Store: "a", Table: "bc"}
	if bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("AAD encoding is ambiguous")
	}
}

// A redial near a key-period boundary must still find its session: the MAC
// made under the previous period key is among the lookup candidates.
func TestLookupMACsMatchesPreviousPeriod(t *testing.T) {
	keys := newMemKeys(t)
	ctx := context.Background()
	// Simulate a MAC computed while "p1" was current.
	old := &memKeys{cur: keys.prev}
	oldMAC, err := NewEnvelope(old, rand.Reader).MAC(ctx, ClassIdentifiers, []byte("+256772123456"))
	if err != nil {
		t.Fatal(err)
	}
	cands, err := NewEnvelope(keys, rand.Reader).LookupMACs(ctx, ClassIdentifiers, []byte("+256772123456"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want current+previous", len(cands))
	}
	found := false
	for _, c := range cands {
		if c.KeyID == oldMAC.KeyID && bytes.Equal(c.Sum, oldMAC.Sum) {
			found = true
		}
	}
	if !found {
		t.Fatal("previous-period MAC not among lookup candidates")
	}
}

func TestIdentifiersSealPhone(t *testing.T) {
	ids := Identifiers{Env: NewEnvelope(newMemKeys(t), rand.Reader)}
	p, _ := pii.ParsePhone("0772123456")
	ctx := context.Background()
	s, err := ids.SealPhone(ctx, p, aad)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := ids.Env.Open(ctx, s, aad)
	if err != nil || string(pt) != "+256772123456" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	m1, _ := ids.PhoneMAC(ctx, p)
	m2, _ := ids.PhoneMAC(ctx, p)
	if !bytes.Equal(m1.Sum, m2.Sum) || len(m1.Sum) != 32 {
		t.Fatal("PhoneMAC is not a stable 32-byte value")
	}
}

func TestMLDSA65SignVerify(t *testing.T) {
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("sauti-audit-checkpoint/v1\norigin\n1\n")
	sig, err := SignMLDSA65(sk, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyMLDSA65(sk.PublicKey().Bytes(), msg, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	sk, _ := mldsa.GenerateKey(mldsa.MLDSA65())
	msg := []byte("body")
	sig, _ := SignMLDSA65(sk, msg)
	if err := VerifyMLDSA65(sk.PublicKey().Bytes(), []byte("bodY"), sig); err == nil {
		t.Fatal("tampered body accepted")
	}
	// A signature made without the checkpoint context must not verify.
	plain, _ := sk.Sign(nil, msg, nil)
	if err := VerifyMLDSA65(sk.PublicKey().Bytes(), msg, plain); err == nil {
		t.Fatal("context-free signature accepted")
	}
}
