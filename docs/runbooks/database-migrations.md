# Runbook: Database migrations

| Field | Value |
|---|---|
| Owner | Platform engineering |
| Severity | SEV-3 (deploy blocker) |
| Alert / trigger | Deploy step `sautictl migrate` fails, or reports `applied migration file has changed` |
| Last reviewed: | 2026-11-13 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

Migrations are forward-only and checksummed (ADR-0010). Each store applies `db/<store>/migrations/NNNN_name.sql` in order, one transaction per file, recorded with its sha256 in `public.schema_migrations`.

## Triggers and detection

* Deploy pipeline output from `sautictl migrate --store <store> --dsn <owner DSN>`.
* Error text `migrate: applied migration file has changed: <store>/<version>`.

## Impact

* A failing migration rolls back entirely and the old schema keeps serving. Services are deployed after their migrations.
* A checksum mismatch blocks every further migration on that store until resolved.
* Before applying anything, the migrator checks the whole recorded history: an applied migration missing from the repository (`ErrMissingMigration`) or edited (`ErrChecksumMismatch`) stops the run with nothing applied.

## Diagnosis

1. On a checksum mismatch, `git log -- db/<store>/migrations/<file>` shows who edited an applied migration. That edit is the bug.
2. On a SQL error, re-run against a fresh development cluster (`sautictl dev-bootstrap --migrate`) to reproduce it.
3. On a SQL error, find out whether the failing file has been applied anywhere. It counts as applied once it is merged, or once any deployment of the store (staging, another site, a shared test server) has run it. Run this as the store owner on each deployment, with the file name minus `.sql`:
   `SELECT version, sha256, applied_at FROM public.schema_migrations WHERE version = '<NNNN_name>';`

## Mitigation

1. Checksum mismatch: revert the edit to the applied file and put the intended change in a new migration.
2. SQL error, file never merged and never applied on any deployment (Diagnosis step 3 finds no row anywhere): fix the file and re-run.
3. SQL error, file merged or applied on some deployment: **do not edit it**. A changed checksum stops every deployment that applied it with `ErrChecksumMismatch` (`docs/how-to/add-a-migration.md`, step 1). The migrator applies files strictly in order, so a later corrective migration cannot run first either. Find what differs on the failing deployment (its data, or schema drift from a manual change), correct that state under a deviation record so that the unchanged file applies, then put any further schema correction in a new migration. Only if the file is not merged and every deployment that applied it is disposable (development, CI) may you rebuild those deployments instead; the file then counts as never applied.
4. Never edit `public.schema_migrations` by hand.
5. A constraint a migration adds `NOT VALID` (for example `incident_events_flag_is_string`, 0027) checks every new write but not the rows already stored, so the migration neither scans a populated table under its lock nor fails on old data. To validate it on a populated store, first count the rows that would fail. For the flag constraint, as `op_readonly`:

   ```sql
   SELECT count(*) FROM op.incident_events
    WHERE type = 'incident.flagged' AND jsonb_typeof(payload -> 'flag') IS DISTINCT FROM 'string';
   ```

   Repair any such rows only by an approved correction under a deviation record. Then, as the schema owner, run `ALTER TABLE op.incident_events VALIDATE CONSTRAINT incident_events_flag_is_string;`. It takes only a `SHARE UPDATE EXCLUSIVE` lock, so reads and writes continue.

## Escalation

Platform lead. For `db/analytic` changes the PI must have approved the pull request (CODEOWNERS); during the study window an amendment record is required.

## Verification

`sautictl migrate` reports `applied 0 migration(s)` on a re-run, and `scripts/ci/integration.sh` passes.

## References

ADR-0010; `internal/platform/pg/migrate/migrate.go`; `docs/how-to/add-a-migration.md`.
