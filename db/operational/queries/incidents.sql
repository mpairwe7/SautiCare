-- name: OpenIncident :one
SELECT o.incident_id::uuid AS incident_id, o.reference::text AS reference, o.event_id::uuid AS event_id,
       o.t1::timestamptz AS t1, o.created::boolean AS created
FROM op.open_incident(
    sqlc.arg(reference)::text, sqlc.arg(channel)::text, sqlc.arg(session_id)::uuid,
    sqlc.arg(provider_session_id)::text, sqlc.narg(phone_ct)::bytea, sqlc.narg(phone_key_id)::text,
    sqlc.narg(phone_mac)::bytea, sqlc.narg(phone_mac_key_id)::text, sqlc.narg(network_code)::text,
    sqlc.arg(idem_key)::text
) AS o(incident_id, reference, event_id, t1, created);

-- name: AppendEvent :one
SELECT a.event_id::uuid AS event_id, a.seq::bigint AS seq, a.recorded_at::timestamptz AS recorded_at,
       a.new_state::text AS new_state, a.duplicate::boolean AS duplicate
FROM op.append_incident_event(
    sqlc.arg(incident_id)::uuid, sqlc.arg(expected_version)::bigint, sqlc.arg(type)::text,
    sqlc.arg(payload)::jsonb, sqlc.arg(actor_kind)::text, sqlc.narg(staff_id)::uuid,
    sqlc.arg(idem_key)::text, sqlc.narg(logic_version)::text
) AS a(event_id, seq, recorded_at, new_state, duplicate);

-- name: ListEvents :many
SELECT id, incident_id, seq, type, recorded_at, actor_kind, staff_id, payload, idem_key, logic_version
FROM op.incident_events
WHERE incident_id = sqlc.arg(incident_id)::uuid
ORDER BY seq;

-- name: GetIncident :one
SELECT id, reference, channel, state, version, severity, logic_version,
       t1, t2, t3, t4, t5, t6, t7, escalated, flags, merged_into
FROM op.incidents
WHERE id = sqlc.arg(id)::uuid;

-- name: ListIncidentIDs :many
SELECT id FROM op.incidents ORDER BY id;

-- name: ClaimOutbox :many
SELECT id, payload FROM op.claim_outbox(sqlc.arg(topic)::text, sqlc.arg(max_rows)::int);

-- name: MarkOutboxDelivered :one
SELECT op.mark_outbox_delivered(sqlc.arg(ids)::uuid[])::int AS marked;

-- name: DefaultPartitionRows :one
SELECT op.default_partition_rows()::bigint AS n;
