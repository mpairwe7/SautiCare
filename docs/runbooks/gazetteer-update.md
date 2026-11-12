# Runbook: Load or correct the gazetteer

| Field | Value |
|---|---|
| Owner | Field Coordinator (content) with the technical operator (load) |
| Severity | SEV-3 (planned change) |
| Alert / trigger | new or corrected ward and landmark lists; intake-gw refusing to start with `opstore: no gazetteer loaded` |
| Last reviewed: | 2026-11-12 |
| Last drilled | not yet drilled |

## Summary
Ward menus (screen 6), landmark menus (screen 7) and free-text matching come from a versioned gazetteer. A loaded version can never be edited in place: a correction is a new version, and during the study window a deviation record.

## Triggers and detection
- Mobilisation delivers the agreed per-ward landmark lists and GPS points.
- A ward reports a wrong or missing landmark.
- intake-gw fails at start-up because no version is loaded.

## Impact
No dispatch-path impact while the version in force stays loaded. A wrong landmark sends responders to the wrong zone. The `needs_location_callback` fallback covers free text only, not a wrong menu entry.

## Diagnosis
1. Version in force: `SELECT version, effective_from FROM op.config_versions WHERE kind = 'gazetteer' ORDER BY effective_from DESC LIMIT 1;`
2. Validate the file without a database: `go test ./internal/location/ -run TestRepositoryGazetteer`.

## Mitigation
1. Edit `config/gated/gazetteer.yaml` and give it a **new `version`**. Set `status: approved` only when the lists are agreed with ward leadership.
2. Run `go test ./internal/intake/ -run TestEveryRenderedScreenFitsLimitAndGSM7`: every ward's landmark menu must fit the operator limit in both languages.
3. During the study window: add the amendment record, and record a deviation for the correction.
4. Load, as the operational store owner: `sautictl gazetteer load --file config/gated/gazetteer.yaml --dsn <owner DSN>`. Loading the same version again is a no-op; different content under a loaded version is refused.
5. Restart intake-gw so it takes the new snapshot.

## Escalation
Field Coordinator and PI for any correction during the window (deviation record).

## Verification
intake-gw's start-up log shows `gazetteer=<new version>`; a test call shows the corrected menu.

## References
ADR-0020; `docs/reference/tool1-flow.md`; Build Prompt §9, §12.
