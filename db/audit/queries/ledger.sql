-- name: AppendEntry :one
SELECT seq, entry_id, occurred_at, event_type, incident_id, staff_id, logic_version, payload_sha256, prev_hash, hash
FROM audit.append(
    sqlc.arg(entry_id)::uuid, sqlc.arg(occurred_at)::timestamptz, sqlc.arg(event_type)::text,
    sqlc.narg(incident_id)::uuid, sqlc.narg(staff_id)::uuid, sqlc.narg(logic_version)::text,
    sqlc.arg(payload_sha256)::bytea);

-- name: Head :many
SELECT h.seq::bigint AS seq, h.hash::bytea AS hash FROM audit.head() AS h(seq, hash);

-- name: SaveCheckpoint :one
SELECT audit.save_checkpoint(
    sqlc.arg(origin)::text, sqlc.arg(seq)::bigint, sqlc.arg(head)::bytea, sqlc.arg(signed_at)::timestamptz,
    sqlc.arg(key_id)::text, sqlc.arg(body)::bytea, sqlc.arg(signature)::bytea)::bigint AS id;

-- name: RangeEntries :many
SELECT seq, entry_id, occurred_at, event_type, incident_id, staff_id, logic_version, payload_sha256, prev_hash, hash
FROM audit.entries
WHERE seq >= sqlc.arg(from_seq)::bigint
ORDER BY seq
LIMIT sqlc.arg(page_size)::int;

-- name: ListCheckpoints :many
SELECT origin, seq, head, signed_at, key_id, signature
FROM audit.checkpoints
ORDER BY id;
