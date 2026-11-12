package crypto

import (
	"bytes"
	"context"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
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

// CodeRabbit PR #2: an empty storage location makes the AAD identical for
// every row, so a ciphertext could move between rows undetected.
func TestSealAndOpenRequireACompleteLocation(t *testing.T) {
	env := NewEnvelope(newMemKeys(t), rand.Reader)
	ctx := context.Background()
	for _, bad := range []AAD{{}, {Store: "operational", Table: "sessions", Column: "phone"}, {Table: "t", Column: "c", RowID: "r"}} {
		if _, err := env.Seal(ctx, ClassIdentifiers, []byte("x"), bad); !errors.Is(err, ErrIncompleteAAD) {
			t.Errorf("Seal(%+v) = %v, want ErrIncompleteAAD", bad, err)
		}
		if _, err := env.Open(ctx, Sealed{KeyID: "p2", Ciphertext: make([]byte, 40)}, bad); !errors.Is(err, ErrIncompleteAAD) {
			t.Errorf("Open(%+v) = %v, want ErrIncompleteAAD", bad, err)
		}
	}
}

// A provider returning a short or empty key must not yield a MAC derived
// from public input (CodeRabbit PR #2).
func TestMACRejectsInvalidKeyLength(t *testing.T) {
	for _, key := range [][]byte{nil, {}, make([]byte, 16), make([]byte, 33)} {
		env := NewEnvelope(&memKeys{cur: DataKey{ID: "bad", Key: key}}, rand.Reader)
		if _, err := env.MAC(context.Background(), ClassIdentifiers, []byte("+256772123456")); err == nil {
			t.Errorf("MAC accepted a %d-byte key", len(key))
		}
	}
}

// Each signature purpose has its own ML-DSA context, so a triage-rules
// signature can never be presented as an audit checkpoint signature (or the
// reverse), even under the same key.
func TestSignatureContextsAreSeparated(t *testing.T) {
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	pub := sk.PublicKey().Bytes()
	msg := []byte("bundle")
	sig, err := SignWithContext(sk, TriageRulesContext, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWithContext(pub, TriageRulesContext, msg, sig); err != nil {
		t.Fatalf("own context: %v", err)
	}
	if err := VerifyMLDSA65(pub, msg, sig); err == nil {
		t.Fatal("triage signature verified as a checkpoint signature")
	}
	cp, err := SignMLDSA65(sk, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyWithContext(pub, TriageRulesContext, msg, cp); err == nil {
		t.Fatal("checkpoint signature verified as a triage signature")
	}
}

// USSD step keys are keyed hashes under their own HKDF info: never the plain
// sha256 of the input (which holds the callback number at screen 8, and
// would be reversible by enumeration), and never equal to the phone lookup
// MAC of the same bytes.
func TestStepMACIsKeyedAndSeparated(t *testing.T) {
	ids := Identifiers{Env: NewEnvelope(newMemKeys(t), rand.Reader)}
	ctx := context.Background()
	msg := []byte("+256772123456")
	step, err := ids.StepMAC(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256(msg)
	phone, err := ids.Env.MAC(ctx, ClassIdentifiers, msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(step.Sum, plain[:]) || bytes.Equal(step.Sum, phone.Sum) || step.KeyID != phone.KeyID {
		t.Fatalf("step MAC not domain-separated: %x", step.Sum)
	}
	cands, err := ids.StepLookupMACs(ctx, msg)
	if err != nil || len(cands) == 0 || !bytes.Equal(cands[0].Sum, step.Sum) {
		t.Fatalf("StepLookupMACs = %v, %v", cands, err)
	}
}

func TestSealTextBindsToItsRow(t *testing.T) {
	ids := Identifiers{Env: NewEnvelope(newMemKeys(t), rand.Reader)}
	ctx := context.Background()
	aad := AAD{Store: "operational", Table: "incident_identifiers", Column: "ct", RowID: "row-1"}
	s, err := ids.SealText(ctx, []byte("Kawempe mkt"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ids.Env.Open(ctx, s, aad); err != nil || string(got) != "Kawempe mkt" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	other := aad
	other.RowID = "row-2"
	if _, err := ids.Env.Open(ctx, s, other); err == nil {
		t.Fatal("ciphertext opened under another row's AAD")
	}
}
