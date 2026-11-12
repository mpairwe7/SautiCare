package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"
	"uuid"
)

func fixtureEntry() Entry {
	inc := uuid.MustParse("0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70")
	staff := uuid.MustParse("0195f3a2-7c1e-7d4b-9a10-000000000001")
	return Entry{
		Seq:           42,
		EntryID:       uuid.MustParse("0195f3a2-7c1e-7d4b-9a10-0000000000aa"),
		OccurredAt:    time.Date(2026, 11, 12, 10, 0, 0, 123456000, time.UTC),
		EventType:     "incident.classified",
		IncidentID:    &inc,
		StaffID:       &staff,
		LogicVersion:  "tr-2026.11.0+sha256:abcdef012345",
		PayloadSHA256: sha256.Sum256([]byte(`{"severity":"red"}`)),
	}
}

// lp writes a 4-byte big-endian length prefix and the bytes — the encoding
// spelled out in docs/reference/audit-canonical-encoding.md, built here
// independently of the implementation.
func lp(buf *bytes.Buffer, b []byte) {
	_ = binary.Write(buf, binary.BigEndian, uint32(len(b)))
	buf.Write(b)
}

func TestCanonicalGoldenVector(t *testing.T) {
	e := fixtureEntry()
	var want bytes.Buffer
	want.WriteString("sauti-audit-entry/v1")
	want.WriteByte(0)
	seq := make([]byte, 8)
	binary.BigEndian.PutUint64(seq, 42)
	lp(&want, seq)
	lp(&want, e.EntryID[:])
	micros := make([]byte, 8)
	binary.BigEndian.PutUint64(micros, uint64(e.OccurredAt.UnixMicro()))
	lp(&want, micros)
	lp(&want, []byte("incident.classified"))
	lp(&want, e.IncidentID[:])
	lp(&want, e.StaffID[:])
	lp(&want, []byte("tr-2026.11.0+sha256:abcdef012345"))
	lp(&want, e.PayloadSHA256[:])

	if got := Canonical(e); !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("Canonical mismatch\n got %x\nwant %x", got, want.Bytes())
	}
}

// A missing incident or staff ID encodes as a zero-length field, which is
// distinct from any present 16-byte UUID.
func TestCanonicalDistinguishesAbsentFields(t *testing.T) {
	a := fixtureEntry()
	b := fixtureEntry()
	b.StaffID = nil
	if bytes.Equal(Canonical(a), Canonical(b)) {
		t.Fatal("absent staff ID encodes like a present one")
	}
}

// Sub-microsecond precision is not stored by PostgreSQL; the encoding must
// use microseconds so that Go and SQL agree.
func TestCanonicalUsesMicroseconds(t *testing.T) {
	a := fixtureEntry()
	b := fixtureEntry()
	b.OccurredAt = b.OccurredAt.Add(999 * time.Nanosecond)
	if !bytes.Equal(Canonical(a), Canonical(b)) {
		t.Fatal("nanoseconds below a microsecond changed the encoding")
	}
}

func TestChainHashGenesisUsesZeroPrev(t *testing.T) {
	e := fixtureEntry()
	var zero [32]byte
	want := sha256.Sum256(append(zero[:], Canonical(e)...))
	if got := ChainHash(zero, e); got != want {
		t.Fatalf("ChainHash = %x, want %x", got, want)
	}
	other := zero
	other[0] = 1
	if ChainHash(other, e) == want {
		t.Fatal("ChainHash ignores the previous hash")
	}
}
