# Runbook: Database migrations

| Field | Value |
|---|---|
| Owner | Platform engineering |
| Severity | SEV-3 (deploy blocker) |
| Alert / trigger | Deploy step `sautictl migrate` fails, or reports `applied migration file has changed` |
| Last reviewed: | 2026-11-12 |
| Last drilled | Not yet drilled (M0, development only) |

## Summary

Migrations are forward-only and checksummed (ADR-0010). Each store applies `db/<store>/migrations/NNNN_name.sql` in order, one transaction per file, recorded with its sha256 in `public.schema_migrations`.

## Triggers and detection

* Deploy pipeline output from `sautictl migrate --store <store> --dsn <owner DSN>`.
* Error text `migrate: applied migration file has changed: <store>/<version>`.

## Impact

* A failing migration rolls back entirely and the old schema keeps serving. Services are deployed after their migrations.
* A checksum mismatch blocks every further migration on that store until resolved.

## Diagnosis

1. On a checksum mismatch, `git log -- db/<store>/migrations/<file>` shows who edited an applied migration. That edit is the bug.
2. On a SQL error, re-run against a fresh development cluster (`sautictl dev-bootstrap --migrate`) to reproduce it.

## Mitigation

1. Checksum mismatch: revert the edit to the applied file and put the intended change in a new migration.
2. SQL error: fix the new migration file (it never applied, so editing it is allowed) and re-run.
3. Never edit `public.schema_migrations` by hand.

## Escalation

Platform lead. For `db/analytic` changes the PI must have approved the pull request (CODEOWNERS); during the study window an amendment record is required.

## Verification

`sautictl migrate` reports `applied 0 migration(s)` on a re-run, and `scripts/ci/integration.sh` passes.

## References

ADR-0010; `internal/platform/pg/migrate/migrate.go`; `docs/how-to/add-a-migration.md`.
