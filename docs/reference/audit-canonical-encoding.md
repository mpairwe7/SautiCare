# Audit ledger canonical encoding (v1)

**Type:** Reference (Diátaxis) · **Normative** · **Owners:** platform engineering · **ADR:** [ADR-0005](../adr/ADR-0005-audit-ledger-checkpoints.md)

Every audit ledger entry's hash is computed over this exact byte string. Two implementations must agree byte for byte:

- `audit.canonical()` in `db/audit/migrations/0001_ledger.sql` computes the hash when a row is appended.
- `Canonical` in `internal/audit/canonical.go` recomputes it during verification (`sautictl verify-ledger`).

The integration test `internal/platform/pg/auditstore/auditstore_integration_test.go::TestSQLHashMatchesGoCanonical` checks that they agree.

## Entry encoding

```
canonical = "sauti-audit-entry/v1" ‖ 0x00
          ‖ lp(int8be(seq))
          ‖ lp(uuid16(entry_id))
          ‖ lp(int8be(occurred_at as Unix microseconds))
          ‖ lp(utf8(event_type))
          ‖ lp(uuid16(incident_id) or empty)
          ‖ lp(uuid16(staff_id) or empty)
          ‖ lp(utf8(logic_version) or empty)
          ‖ lp(payload_sha256, 32 bytes)

lp(x)  = int4be(len(x)) ‖ x          -- 4-byte big-endian length prefix
hash   = sha256(prev_hash ‖ canonical)
prev_hash of seq 1 = 32 zero bytes
```

Rules:

- **Absent values.** An absent incident ID, staff ID or logic version is encoded as a zero-length field. A present UUID is always 16 bytes, so absent and present values can never collide. The ledger's `CHECK (logic_version <> '')` keeps an empty logic version distinct from "no logic version".
- **Timestamp precision.** Timestamps are encoded as microseconds since the Unix epoch, because PostgreSQL stores `timestamptz` at microsecond resolution. Any finer precision from Go is discarded.
- **Payload hash.** `payload_sha256` is `sha256` of the operational event payload's `jsonb::text`, computed by `op.append_incident_event`. The ledger stores this hash and never the payload, so no caller identifier can enter the audit store (hard constraint 8).

## Checkpoint body

A checkpoint is signed with ML-DSA-65 using the context string `sauti-audit-checkpoint/v1`. The signature covers exactly this body:

```
sauti-audit-checkpoint/v1\n
<origin>\n
<seq, decimal>\n
<base64 standard encoding of the 32-byte chain head>\n
<signed_at, RFC 3339 with nanoseconds, UTC>\n
```

`signed_at` is truncated to whole microseconds before signing, so the body re-derived from the stored row matches what was signed.

## Changing the encoding

The encoding is versioned by its domain string. Changing it means:

1. A new domain string, such as `sauti-audit-entry/v2`.
2. A new ADR.
3. A migration that marks the sequence number where the new format starts.

Existing entries are never re-hashed.
