# Environment variables

**Type:** Reference (Diátaxis) · Template: `.env.example`

Processes read deployment settings from the environment (12-factor), so a later move to Kubernetes is a re-host. No secret is ever committed. In production, OpenBao agent renders credentials onto each machine (P0).

| Variable | Used by | Meaning | Default |
|---|---|---|---|
| `SAUTI_ENV` | all | `dev`, `test` or `production`; production refuses dev keys and pending protocol content; `sautictl demo m0` requires `dev` or `test` | `dev` |
| `SAUTI_CONFIG_DIR` | intake-gw, outbox-relay, dispatch-worker | path to `config/` | `config` |
| `SAUTI_DEVKEYS_DIR` | intake-gw, outbox-relay, dispatch-worker | development key directory (`sautictl dev-keygen`) | `.devkeys` |
| `SAUTI_OP_DSN` | intake-gw (`op_intake`), outbox-relay (`op_relay`), dispatch-worker (`op_dispatch`) | operational store DSN, least-privileged role | — (required) |
| `SAUTI_DSN` | sautictl escalations | the `op_readonly` DSN when `--dsn` is not given | — |
| `SAUTI_AUDIT_DSN` | outbox-relay (`audit_appender`) | audit store DSN | — (required) |
| `SAUTI_HTTP_ADDR` | intake-gw / outbox-relay | callback listener / health and metrics listener | `:8080` / `127.0.0.1:9101` |
| `SAUTI_ADMIN_ADDR` | intake-gw / ivr-controller / dispatch-worker | health and metrics listener | `127.0.0.1:9100` / `127.0.0.1:9101` / `127.0.0.1:9102` |
| `SAUTI_ARI_URL` | ivr-controller | Asterisk HTTP server carrying ARI; loopback only | `http://127.0.0.1:8088` |
| `SAUTI_ARI_USER` | ivr-controller | ARI user | `sauti` |
| `SAUTI_ARI_PASSWORD` | ivr-controller | ARI password (secret: OpenBao in production) | required |
| `SAUTI_ARI_APP` | ivr-controller | Stasis application the dialplan hands calls to; after a restart or a reconnect, calls left in it with no worker are handed to a coordinator | `sauti-ivr` |
| `SAUTI_ARI_ALERT_APP` | ivr-controller | Stasis application of outbound alert calls | `sauti-alerts` |
| `SAUTI_ARI_DIAL` | ivr-controller | endpoint template for alert calls with `{number}` (e.g. `PJSIP/{number}@sauti-trunk`; the trunk is provisioned in P0); unset: no voice alerts | — |
| `SAUTI_ARI_CALLER_ID` | ivr-controller | Sauti's number: what responders see on an alert call, what both parties of the masked bridge see, and what coordinators see on escalation calls (placeholder until P0); unset: no masked bridge | — |
| `SAUTI_CALLER_ID` | Asterisk dialplan (`[globals]` in `telephony/asterisk/extensions.conf`, not a process variable) | the number a coordinator sees on a call the fallback puts through, set before it answers or dials; P0 sets it to the value of `SAUTI_ARI_CALLER_ID`. The name is "SAUTI" and the reference (`SAUTI_REF`, set by ivr-controller). Empty: no number is shown, never the caller's (rule 5; ADR-0034) | empty |
| `SAUTI_COORDINATOR_DIAL` | ivr-controller (and the dialplan fallback) | the coordinator line rung about every escalation until a coordinator presses 1 (an endpoint; placeholder until P0); works with or without `SAUTI_ARI_DIAL`; unset: escalations are not rung | — |
| `SAUTI_COORDINATOR_BACKUP_DIAL` | ivr-controller | tier 2: the backup line rung as well once an escalation has had `escalation.backup_after_calls` unanswered rings; the first 1 on either acknowledges (an endpoint; placeholder until P0; ADR-0034); unset: a start-up warning, and only the coordinator line rings (UX-7 refuses production without it) | — |
| `SAUTI_COORDINATOR_LINES` | ivr-controller | how many calls the coordinator line takes at once (1–50); never more are ringing or answered at once, the most severe escalation first | `1` |
| `SAUTI_OUTBOUND_NO_INPUT` | ivr-controller | how long an answered forwarding or coordinator call may go without a key it offers before Sauti hangs up, freeing the line (a Go duration, `30s`–`10m`); the escalation, unacknowledged, rings again at its 2-minute interval | `2m` |
| `SAUTI_AT_PATH_TOKEN` | intake-gw | secret callback path token, ≥ 24 characters | — (required) |
| `SAUTI_AT_DELIVERY_TOKEN` | intake-gw | secret path token of the SMS delivery-report callback (`/v1/sms/delivery/at/<token>`), ≥ 24 characters; unset: not served | — |
| `SAUTI_AT_API_KEY` | dispatch-worker | Africa's Talking API key (secret: OpenBao in production); unset: simulated delivery, development and tests only | — |
| `SAUTI_AT_USERNAME` | dispatch-worker | Africa's Talking application username | `sandbox` |
| `SAUTI_AT_SMS_URL` | dispatch-worker | messaging base URL (the egress allowlist holds exactly its host) | `https://api.sandbox.africastalking.com` |
| `SAUTI_AT_SENDER_ID` | dispatch-worker | optional alphanumeric sender ID (≤ 11) | — |
| `SAUTI_USSD_SERVICE_CODES` | intake-gw | comma-separated accepted service codes | — (required) |
| `SAUTI_USSD_RESPONDER_CODES` | intake-gw | comma-separated service codes of the responder menu (accept, decline, availability); unset: no responder menu | — |
| `SAUTI_RULES_ROOT` | intake-gw | directory holding `triage-rules/` (the signed bundle named by `config/gated/triage.yaml`) | `.` |
| `SAUTI_WITNESS_DIR` | outbox-relay | write-once checkpoint witness directory | `var/witness` |
| `SAUTI_ORIGIN` | outbox-relay | ledger origin name in checkpoints | `sauti-dispatch` |
| `SAUTI_DEV_DB_PASSWORD` | sautictl dev-bootstrap, sautictl demo | the dev server's password for all Sauti roles. Roles are server-global, so the demo reuses it and never changes it. | — (required) |
| `SAUTI_TEST_PG_DSN` | tests, `sautictl demo m0` | superuser URL of a shared PostgreSQL 18 test server; unset → each test process starts a private embedded PostgreSQL with random credentials | — |
| `SAUTI_TEST_ROLE_PASSWORD` | tests | role password on a shared test server; **required with `SAUTI_TEST_PG_DSN`**, because every test process shares the roles (CI derives it from the run ID) | — |
