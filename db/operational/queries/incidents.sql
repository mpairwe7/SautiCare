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
       t1, t2, t3, t4, t5, t6, t7, escalated, flags, merged_into,
       language, danger, category, age_band, specific_danger, ward, landmark,
       last_screen, zone_col, zone_row, location_quality
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

-- name: LookupUSSDStep :many
SELECT response, end_session, consumed_len, input_mac, mac_key_id, incident_id, prompt_kind FROM op.ussd_steps
WHERE provider = sqlc.arg(provider)::text AND provider_session_id = sqlc.arg(provider_session_id)::text
  AND input_mac = ANY (sqlc.arg(input_macs)::bytea[]);

-- name: LatestUSSDStep :many
SELECT response, end_session, consumed_len, input_mac, mac_key_id, incident_id, prompt_kind FROM op.ussd_steps
WHERE provider = sqlc.arg(provider)::text AND provider_session_id = sqlc.arg(provider_session_id)::text
ORDER BY consumed_len DESC, created_at DESC
LIMIT 1;

-- name: SaveUSSDStep :exec
INSERT INTO op.ussd_steps (provider, provider_session_id, input_mac, mac_key_id, consumed_len, response, end_session,
                           incident_id, prompt_kind)
VALUES (sqlc.arg(provider)::text, sqlc.arg(provider_session_id)::text, sqlc.arg(input_mac)::bytea,
        sqlc.arg(mac_key_id)::text, sqlc.arg(consumed_len)::int, sqlc.arg(response)::text,
        sqlc.arg(end_session)::boolean, sqlc.narg(incident_id)::uuid, sqlc.arg(prompt_kind)::text)
ON CONFLICT DO NOTHING;

-- name: StoreIdentifier :one
SELECT op.store_identifier(sqlc.arg(id)::uuid, sqlc.arg(incident_id)::uuid, sqlc.arg(kind)::text,
                           sqlc.arg(ct)::bytea, sqlc.arg(key_id)::text, sqlc.arg(idem_key)::text)::uuid AS id;

-- name: ResumeSession :one
SELECT r.incident_id::uuid AS incident_id, r.reference::text AS reference, r.event_id::uuid AS event_id,
       r.t1::timestamptz AS t1, r.created::boolean AS created
FROM op.resume_session(
    sqlc.arg(incident_id)::uuid, sqlc.arg(channel)::text, sqlc.arg(session_id)::uuid,
    sqlc.arg(provider_session_id)::text, sqlc.narg(phone_ct)::bytea, sqlc.narg(phone_key_id)::text,
    sqlc.narg(phone_mac)::bytea, sqlc.narg(phone_mac_key_id)::text, sqlc.narg(network_code)::text,
    sqlc.arg(idem_key)::text
) AS r(incident_id, reference, event_id, t1, created);

-- name: FindResumable :many
SELECT i.id FROM op.sessions s JOIN op.incidents i ON i.id = s.incident_id
WHERE s.phone_mac = ANY (sqlc.arg(macs)::bytea[])
  AND i.state NOT IN ('closed', 'merged') AND i.last_screen < 8
  AND i.updated_at >= clock_timestamp() - sqlc.arg(window_seconds)::int * interval '1 second'
ORDER BY i.updated_at DESC
LIMIT 1;

-- name: ListIdle :many
SELECT id, danger, category, (t2 IS NOT NULL)::boolean AS dispatched FROM op.incidents
WHERE state NOT IN ('closed', 'merged') AND last_screen < 8
  AND updated_at < clock_timestamp() - sqlc.arg(idle_seconds)::int * interval '1 second'
  AND NOT ('abandoned' = ANY (flags))
ORDER BY updated_at
LIMIT 500;
