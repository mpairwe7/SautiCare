# Environment variables

**Type:** Reference (Diátaxis) · Template: `.env.example`

Processes read deployment settings from the environment (12-factor), so a later move to Kubernetes is a re-host. No secret is ever committed. In production, OpenBao agent renders credentials onto each machine (P0).

| Variable | Used by | Meaning | Default |
|---|---|---|---|
| `SAUTI_ENV` | all | `dev`, `test` or `production`; production refuses dev keys and pending protocol content; `sautictl demo m0` requires `dev` or `test` | `dev` |
| `SAUTI_CONFIG_DIR` | intake-gw, outbox-relay | path to `config/` | `config` |
| `SAUTI_DEVKEYS_DIR` | intake-gw, outbox-relay | development key directory (`sautictl dev-keygen`) | `.devkeys` |
| `SAUTI_OP_DSN` | intake-gw (`op_intake`), outbox-relay (`op_relay`) | operational store DSN, least-privileged role | — (required) |
| `SAUTI_AUDIT_DSN` | outbox-relay (`audit_appender`) | audit store DSN | — (required) |
| `SAUTI_HTTP_ADDR` | intake-gw / outbox-relay | callback listener / health and metrics listener | `:8080` / `127.0.0.1:9101` |
| `SAUTI_ADMIN_ADDR` | intake-gw | health and metrics listener | `127.0.0.1:9100` |
| `SAUTI_AT_PATH_TOKEN` | intake-gw | secret callback path token, ≥ 24 characters | — (required) |
| `SAUTI_USSD_SERVICE_CODES` | intake-gw | comma-separated accepted service codes | — (required) |
| `SAUTI_DEFAULT_LANG` | intake-gw | `en` or `lg` until screen 1 exists (M1) | `en` |
| `SAUTI_WITNESS_DIR` | outbox-relay | write-once checkpoint witness directory | `var/witness` |
| `SAUTI_ORIGIN` | outbox-relay | ledger origin name in checkpoints | `sauti-dispatch` |
| `SAUTI_DEV_DB_PASSWORD` | sautictl dev-bootstrap, sautictl demo | the dev server's password for all Sauti roles. Roles are server-global, so the demo reuses it and never changes it. | — (required) |
| `SAUTI_TEST_PG_DSN` | tests, `sautictl demo m0` | superuser URL of a shared PostgreSQL 18 test server; unset → each test process starts a private embedded PostgreSQL with random credentials | — |
| `SAUTI_TEST_ROLE_PASSWORD` | tests | role password on a shared test server; **required with `SAUTI_TEST_PG_DSN`**, because every test process shares the roles (CI derives it from the run ID) | — |
