# How to add a database migration

**Type:** How-to (Diátaxis) · **ADR:** [ADR-0010](../adr/ADR-0010-forward-only-migrator.md) · **Runbook:** [database-migrations](../runbooks/database-migrations.md)

1. Create the next file in the store's directory, for example `db/operational/migrations/<NNNN>_short_name.sql`, using lower-case snake case. **Never edit a file that has already been merged:** applied migrations are checksummed and the migrator refuses to run if one changes.
2. Write forward-only SQL:
   - Grant least privilege explicitly, and `REVOKE … FROM PUBLIC` for new functions.
   - Give every `SECURITY DEFINER` function `SET search_path = <schema>, pg_temp`.
   - Set timestamps with `clock_timestamp()`, and keep them out of runtime roles' INSERT grants: grant INSERT on the data columns only.
   - A table whose runtime roles hold column-level INSERT (for example `op.ussd_steps` since 0027, and the analytic, linkage and media writer tables) gives a new column no INSERT privilege: grant it explicitly in the migration that adds the column.
3. If the statement uses PostgreSQL 18 syntax that sqlc v1.31.1 cannot parse (`WITHOUT OVERLAPS`), put it inside `DO $$ BEGIN EXECUTE '…'; END $$;` with a comment pointing at ADR-0011.
4. Add or adjust queries in `db/<store>/queries/`, then run `scripts/tools/install-sqlc.sh && bin/sqlc generate` and commit the generated code.
5. Write the integration test first, then run it: `go test -tags integration ./internal/platform/pg/...`.
6. For `db/analytic/`: the PI must approve the pull request (CODEOWNERS), and during the study window an amendment record is required.
7. Update `docs/reference/stores.md` if roles or invariants changed.
