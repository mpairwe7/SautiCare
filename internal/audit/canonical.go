package audit

import (
	"crypto/sha256"
	"encoding/binary"
)

// entryDomain prefixes every canonical encoding (version-tagged so a future
// format change cannot collide with v1 hashes).
const entryDomain = "sauti-audit-entry/v1"

func appendLP(dst, b []byte) []byte {
	// Fields are at most a few hundred bytes (CHECKs in the ledger schema).
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(b))) //nolint:gosec // bounded length
	return append(dst, b...)
}

// Canonical returns the normative byte encoding of an entry, identical to
// audit.canonical() in db/audit/migrations/0001_ledger.sql. See
// docs/reference/audit-canonical-encoding.md. Absent incident/staff IDs and
// an empty logic version encode as zero-length fields; the timestamp is
// encoded in microseconds, PostgreSQL's precision.
func Canonical(e Entry) []byte {
	out := make([]byte, 0, 160)
	out = append(out, entryDomain...)
	out = append(out, 0)
	// int8send in SQL is two's complement; the bit-identical conversion matches it.
	out = appendLP(out, binary.BigEndian.AppendUint64(nil, uint64(e.Seq))) //nolint:gosec // two's-complement encoding
	out = appendLP(out, e.EntryID[:])
	out = appendLP(out, binary.BigEndian.AppendUint64(nil, uint64(e.OccurredAt.UnixMicro()))) //nolint:gosec // two's-complement encoding
	out = appendLP(out, []byte(e.EventType))
	var inc, staff []byte
	if e.IncidentID != nil {
		inc = e.IncidentID[:]
	}
	if e.StaffID != nil {
		staff = e.StaffID[:]
	}
	out = appendLP(out, inc)
	out = appendLP(out, staff)
	out = appendLP(out, []byte(e.LogicVersion))
	out = appendLP(out, e.PayloadSHA256[:])
	return out
}

// ChainHash returns sha256(prev ‖ Canonical(e)). The first entry's prev is
// 32 zero bytes.
func ChainHash(prev [32]byte, e Entry) [32]byte {
	h := sha256.New()
	h.Write(prev[:])
	h.Write(Canonical(e))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
