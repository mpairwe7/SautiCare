# Africa's Talking USSD adapter

**Type:** Reference (Diátaxis) · **Code:** `internal/platform/africastalking/ussd.go` · **Runbook:** [intake-gateway-errors](../runbooks/intake-gateway-errors.md)

## Contract

- **Route:** `POST /v1/ussd/at/{token}` on the intake-gw listener, behind HAProxy.
- **Request:** `application/x-www-form-urlencoded` with these fields:
  - `sessionId`
  - `serviceCode`
  - `phoneNumber`
  - `text`: every input so far, joined with `*`; empty on the first request
  - `networkCode`: MCC+MNC, e.g. `64110` for MTN Uganda or `64101` for Airtel Uganda (not yet confirmed by Africa's Talking)
- **Response:** HTTP 200, `text/plain; charset=utf-8`. The body is `CON <screen>` to continue or `END <screen>` to finish.

## Security (Africa's Talking does not sign callbacks)

| Check | Failure |
|---|---|
| Secret path token, compared in constant time; at least 24 characters | 404, indistinguishable from a missing route |
| `serviceCode` in `SAUTI_USSD_SERVICE_CODES` | 403 |
| Content type must be form-encoded | 415 |
| Body ≤ 8 KiB | 413 |
| Source IP in the aggregator allowlist | enforced at HAProxy and nftables (P0) |

Once the token and service code show the request is ours, nothing else is answered with a 4xx: the caller always gets a screen (fail safe to a human, rule 3.14).

| Authenticated request with… | Answer |
|---|---|
| malformed form encoding, missing or invalid `sessionId`, or `text` over 1024 characters or containing line breaks | `END` with the internal-error copy (the free voice line) |
| an unparseable or withheld `phoneNumber` | processed normally with an **unknown** phone; dispatch proceeds on location |

## Behaviour

- **Idempotency.** The reply to each `(provider, sessionId, sha256(text))` is stored in `op.ussd_steps`, and the first stored reply wins. A retried or concurrent duplicate callback gets the identical screen and writes nothing new.
- **Budget.** Each callback has 800 ms end to end (spec §7.5, p99 under 1 s). No external call happens on this path.
- **Fail safe.** Any internal error returns `END` with the internal-error copy, which gives the free voice line, and is logged with the session ID only.
- **M0 scope.** The first callback opens the incident (reference and database-set T1) and replies in test mode. The Tool 1 eight-screen flow arrives in M1.
- **Screen length.** Every catalogue screen, rendered, must fit the configured limit (160 by default). `internal/intake/service_test.go::TestScreensFitCharLimit` fails the build otherwise.
