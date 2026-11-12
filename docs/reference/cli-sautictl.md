# sautictl command reference

**Type:** Reference (Diátaxis) · **Code:** `cmd/sautictl/main.go`

Every subcommand uses the least-privileged credentials it needs and exits non-zero on failure. The development-only commands refuse `SAUTI_ENV=production`.

| Command | Credentials | Purpose |
|---|---|---|
| `migrate --store <s> --dsn <owner DSN>` | store owner | apply pending forward-only migrations ([ADR-0010](../adr/ADR-0010-forward-only-migrator.md)) |
| `dev-bootstrap --admin-dsn <superuser URL> [--suffix S] [--migrate]` | superuser (dev/CI only) | create the five databases and every role; password from `SAUTI_DEV_DB_PASSWORD` |
| `dev-keygen --dir <dir>` | — (dev/CI only) | write development data keys and the ML-DSA-65 seed (0600) |
| `gazetteer load --file <gazetteer.yaml> --dsn <owner DSN>` | operational store owner | validate the file (before connecting), then store it as an immutable version and make it the version in force; the same version again is a no-op, different content under a loaded version is refused ([ADR-0020](../adr/ADR-0020-location-grid-and-gazetteer.md), [runbook](../runbooks/gazetteer-update.md)) |
| `incident split --dsn <op_console DSN> --survivor <id> --member <id> --staff <UUID>` | `op_console` | split a wrongly merged call off its survivor: `incident.split` and `incident.unmerged`, both with the coordinator's staff identity; idempotent per merge (a retry is a no-op; a later merge of the same pair is split again) ([ADR-0022](../adr/ADR-0022-deduplication-merge-and-split.md), [runbook](../runbooks/split-wrong-merge.md)) |
| `triage keygen --out <file> --key-id dev-<name>` | — (dev/CI only) | create a development rule-signing seed (0600, never overwritten) and print its public key; only `dev-` key IDs, which production refuses ([ADR-0019](../adr/ADR-0019-signed-triage-decision-table.md)) |
| `triage sign --key <file> --key-id <id> --rules <bundle>` | rule signer | write `<bundle>.sig`; refuses to sign a bundle that would not load (signature, totality). Production bundles are signed with the clinical key in OpenBao (P0) |
| `verify-ledger --dsn <audit_reader> (--devkeys-dir D \| --pubkey-file F) (--witness-dir W \| --no-witness)` | `audit_reader` | recompute every hash and check every checkpoint and **witness**; non-zero on any problem. The witness is required unless `--no-witness` explicitly accepts the weaker check ([ADR-0005](../adr/ADR-0005-audit-ledger-checkpoints.md), [runbook](../runbooks/audit-ledger-verification-failure.md)) If the witness directory holds no notes, it prints the same warning as `--no-witness`. |
| `verify-projections --dsn <op_readonly>` | `op_readonly` | prove every projection equals the fold of its events ([runbook](../runbooks/projection-drift.md)) |
| `checkpoint --dsn <audit_appender> --devkeys-dir D --witness-dir W [--origin O]` | `audit_appender` | sign and witness the current chain head now, anchored on the latest verified witness note; `--witness-dir` is required, and the command refuses to sign if the witness does not verify or the chain diverged from it |
| `demo m0 --admin-dsn <superuser URL> [--config-dir config] [--keep]` | superuser (dev only) | run the M0 milestone demo in isolated databases, authenticating the roles with `SAUTI_DEV_DB_PASSWORD` (never changing it) ([how-to](../how-to/run-the-m0-demo.md)) Requires `SAUTI_ENV=dev` or `test` (refuses production before connecting); the run is bounded to 10 minutes and each cleanup step to 30 seconds. |

Exit codes: 0 means success, 1 means the command failed (including a verification failure), and 2 means usage error.
