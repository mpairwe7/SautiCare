# sautictl command reference

**Type:** Reference (Diátaxis) · **Code:** `cmd/sautictl/main.go`

Every subcommand uses the least-privileged credentials it needs and exits non-zero on failure. The development-only commands refuse `SAUTI_ENV=production`.

| Command | Credentials | Purpose |
|---|---|---|
| `migrate --store <s> --dsn <owner DSN>` | store owner | apply pending forward-only migrations ([ADR-0010](../adr/ADR-0010-forward-only-migrator.md)) |
| `dev-bootstrap --admin-dsn <superuser URL> [--suffix S] [--migrate]` | superuser (dev/CI only) | create the five databases and every role; password from `SAUTI_DEV_DB_PASSWORD` |
| `dev-keygen --dir <dir>` | — (dev/CI only) | write development data keys and the ML-DSA-65 seed (0600) |
| `verify-ledger --dsn <audit_reader> (--devkeys-dir D \| --pubkey-file F) (--witness-dir W \| --no-witness)` | `audit_reader` | recompute every hash and check every checkpoint and **witness**; non-zero on any problem. The witness is required unless `--no-witness` explicitly accepts the weaker check ([ADR-0005](../adr/ADR-0005-audit-ledger-checkpoints.md), [runbook](../runbooks/audit-ledger-verification-failure.md)) |
| `verify-projections --dsn <op_readonly>` | `op_readonly` | prove every projection equals the fold of its events ([runbook](../runbooks/projection-drift.md)) |
| `checkpoint --dsn <audit_appender> --devkeys-dir D --witness-dir W [--origin O]` | `audit_appender` | sign and witness the current chain head now |
| `demo m0 --admin-dsn <superuser URL> [--config-dir config] [--keep]` | superuser (dev only) | run the M0 milestone demo in isolated databases, authenticating the roles with `SAUTI_DEV_DB_PASSWORD` (never changing it) ([how-to](../how-to/run-the-m0-demo.md)) |

Exit codes: 0 means success, 1 means the command failed (including a verification failure), and 2 means usage error.
