# Environment variables

**Type:** Reference (Diátaxis) · Template: `.env.example`

Processes read deployment settings from the environment (12-factor), so a later move to Kubernetes is a re-host. No secret is ever committed. In production, OpenBao agent renders credentials onto each machine (P0).

| Variable | Used by | Meaning | Default |
|---|---|---|---|
| `SAUTI_ENV` | all | `dev`, `test` or `production`; production refuses dev keys and pending protocol content; `sautictl demo m0` requires `dev` or `test` | `dev` |
| `SAUTI_CONFIG_DIR` | intake-gw, outbox-relay, dispatch-worker | path to `config/` | `config` |
| `SAUTI_DEVKEYS_DIR` | intake-gw, outbox-relay | development key directory (`sautictl dev-keygen`) | `.devkeys` |
| `SAUTI_OP_DSN` | intake-gw (`op_intake`), outbox-relay (`op_relay`), dispatch-worker (`op_dispatch`) | operational store DSN, least-privileged role | — (required) |
| `SAUTI_AUDIT_DSN` | outbox-relay (`audit_appender`) | audit store DSN | — (required) |
| `SAUTI_HTTP_ADDR` | intake-gw / outbox-relay | callback listener / health and metrics listener | `:8080` / `127.0.0.1:9101` |
| `SAUTI_ADMIN_ADDR` | intake-gw / ivr-controller / dispatch-worker | health and metrics listener | `127.0.0.1:9100` / `127.0.0.1:9101` / `127.0.0.1:9102` |
| `SAUTI_ARI_URL` | ivr-controller | Asterisk HTTP server carrying ARI; loopback only | `http://127.0.0.1:8088` |
| `SAUTI_ARI_USER` | ivr-controller | ARI user | `sauti` |
| `SAUTI_ARI_PASSWORD` | ivr-controller | ARI password (secret: OpenBao in production) | required |
| `SAUTI_ARI_APP` | ivr-controller | Stasis application the dialplan hands calls to | `sauti-ivr` |
| `SAUTI_AT_PATH_TOKEN` | intake-gw | secret callback path token, ≥ 24 characters | — (required) |
| `SAUTI_USSD_SERVICE_CODES` | intake-gw | comma-separated accepted service codes | — (required) |
| `SAUTI_RULES_ROOT` | intake-gw | directory holding `triage-rules/` (the signed bundle named by `config/gated/triage.yaml`) | `.` |
| `SAUTI_WITNESS_DIR` | outbox-relay | write-once checkpoint witness directory | `var/witness` |
| `SAUTI_ORIGIN` | outbox-relay | ledger origin name in checkpoints | `sauti-dispatch` |
| `SAUTI_DEV_DB_PASSWORD` | sautictl dev-bootstrap, sautictl demo | the dev server's password for all Sauti roles. Roles are server-global, so the demo reuses it and never changes it. | — (required) |
| `SAUTI_TEST_PG_DSN` | tests, `sautictl demo m0` | superuser URL of a shared PostgreSQL 18 test server; unset → each test process starts a private embedded PostgreSQL with random credentials | — |
| `SAUTI_TEST_ROLE_PASSWORD` | tests | role password on a shared test server; **required with `SAUTI_TEST_PG_DSN`**, because every test process shares the roles (CI derives it from the run ID) | — |
