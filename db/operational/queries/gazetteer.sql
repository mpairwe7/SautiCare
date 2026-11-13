-- name: CurrentGazetteer :many
SELECT v.version, v.status
FROM op.config_versions c JOIN op.gazetteer_versions v ON v.version = c.version
WHERE c.kind = 'gazetteer'
ORDER BY c.effective_from DESC, c.version DESC
LIMIT 1;

-- name: ListWards :many
SELECT id, name_en, name_lg, centre_col, centre_row FROM op.wards
WHERE version = sqlc.arg(version)::text ORDER BY menu_order;

-- name: ListWardZones :many
SELECT ward_id, zone_col, zone_row FROM op.ward_zones
WHERE version = sqlc.arg(version)::text ORDER BY ward_id, zone_row, zone_col;

-- name: ListLandmarks :many
SELECT id, ward_id, name_en, name_lg, lat, lon, zone_col, zone_row, menu FROM op.landmarks
WHERE version = sqlc.arg(version)::text ORDER BY list_order;

-- name: ListAliases :many
SELECT landmark_id, alias_norm FROM op.landmark_aliases
WHERE version = sqlc.arg(version)::text ORDER BY landmark_id, alias_order;

-- name: MatchLandmark :many
SELECT m.landmark_id::text AS landmark_id, m.similarity::real AS similarity
FROM op.match_landmark(sqlc.arg(version)::text, sqlc.arg(ward_id)::text, sqlc.arg(text)::text) AS m(landmark_id, similarity);

-- name: ListStages :many
SELECT id, ward_id, name_en, name_lg, lat, lon, zone_col, zone_row FROM op.stages
WHERE version = sqlc.arg(version)::text ORDER BY list_order;

-- name: ListFacilities :many
SELECT id, name_en, name_lg, lat, lon, zone_col, zone_row, phone, forwarding_enabled FROM op.facilities
WHERE version = sqlc.arg(version)::text ORDER BY list_order;
