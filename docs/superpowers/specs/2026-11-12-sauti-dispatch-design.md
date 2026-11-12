# Sauti Dispatch: System Design and M0 Specification

| | |
|---|---|
| Date | 2026-11-12 |
| Status | Proposed, awaiting review |
| Scope of this cycle | Whole-system architecture (all milestones, at architecture level) + detailed specification of milestone **M0** |
| Branch | `m0-foundation` |

## 1. Sources and precedence

This design implements the Sauti Dispatch Build Prompt v0.2 (12.11.2026). That prompt draws on the other sources below, and they govern in this order:

1. **Behaviour**: Study Protocol v2.0 and Data Collection Tools 1–7. *These are not yet in the repository.* The Engineering Guide V2 (01.10.2026, §§4–14) explains them and is used for what it states about the protocol.
2. **Data handling**: Information Security Policy v3.0 (ISP), Records Management and Retention Policy (RMRP), Privacy Notice (PN) and Data Protection Impact Assessment (DPIA), all dated 15.07.2026.
3. **Technology**: "Sauti Dispatch — Scalable MVP Stack Without Kubernetes" (10.11.2026), except where Build Prompt §5 overrides it.
4. **Build order and process**: Build Prompt §0 and §18. Build one milestone at a time and stop for review after each. Never invent an answer to an open decision. Where the prompt and a governing document disagree, stop and ask.

The governing documents are confidential and are **not committed**; `.gitignore` excludes them. This repository is public, so documents cite them only by title and section number, never by verbatim text or personal contact details.

## 2. Decisions taken during design (2026-11-12)

| # | Topic | Decision | Recorded in |
|---|---|---|---|
| D1 | Scope of this cycle | Whole-system spec plus M0 only, then stop for review | this spec |
| D2 | Repository and CI host | GitHub (`mpairwe7/SautiCare`) as the interim host, holding code and synthetic data only. The CI logic is written so that Forgejo Actions can run the same scripts, and the move to Forgejo in Uganda happens at P0. PRs are gated on CI and CodeRabbit. | ADR-0008 |
| D3 | Missing protocol documents | The Engineering Guide V2 supplies the 10 MEDS fields and consent states. Tool 1 wording, the Tool 2 codes and the Tool 4 scripts are still needed **before M1**. | §12 |
| D4 | Redial resume window | **10 minutes**, on either channel (Guide §4). Build Prompt §10.1 said 30 min. This is a versioned config value behind the amendment gate. | ADR-0009 |
| D5 | When dispatch starts (T2) | **After screen 7**, as Build Prompt §10.1 says. The trigger screen is versioned config, and the T2 definition in force is stamped on every incident. Flagged for the PI before the analysis plan is registered (Guide §17). | ADR-0009 |
| D6 | Analytic enclave | Primary analysis is descriptive (Guide §11). **PyMC stays installed** for exploratory work only, labelled as such. | ADR-0009 |
| D7 | Architecture | Approach A: a hexagonal modular monolith, a single write path enforced by the database, and a transactional outbox | ADR-0002..0004 |
| D8 | Migrations | An in-house, forward-only, checksummed migrator using only the standard library | ADR-0010 |
| D9 | Incident reference | 9 random digits plus a Damm check digit, read 3-3-4 (Build Prompt §9, which supersedes the Guide's 8+1 proposal; the Guide leaves this choice to engineering) | ADR-0007 |
| D10 | Delivery | All milestones and phases are implemented in sequence (user, 2026-11-12). Each phase has its own detailed plan, ADRs, runbooks, traceability updates and a PR gated on CI and CodeRabbit. See `docs/superpowers/plans/2026-11-12-phased-roadmap.md`. | roadmap |

## 3. Discrepancies and open items register

| # | Item | Sources in tension | Handling |
|---|---|---|---|
| O1 | Ranking weights (decision 4) | Open with the PI and co-investigators | Default order: zone distance, then history, then least recently dispatched. Kept in config and stamped on every incident. |
| O2 | Network-initiated USSD (decision 8) | Build Prompt says "yes". Research (2026-11-12) found **no documented Africa's Talking network-initiated USSD API**. | Hidden behind the `UssdPush` port. Without written confirmation from MTN, Airtel or AT, the alert ladder runs SMS at 0 s and voice at 20 s. Risk owner: Ops. |
| O3 | Dedicated USSD code (decision 9) | UCC reserves 110–119 and 900–999 for emergency services. Guide §4 says Sauti never uses reserved emergency ranges. | The code is requested from the USSD service range (200–298). Placeholder in config; allocating it is a Gate A condition. |
| O4 | 45-second session target | Guide §4: still open with the clinical team | No screen is removed to meet a time limit. Abandonment by screen is measured. |
| O5 | Whether designed payment holds count against payment reliability | Guide §8: open with the PI | M4 reports it both ways |
| O6 | Behaviour while dispatch is suspended under the stopping rules | Guide §14 requires the suspension control but does not script what the caller hears | Must be decided by the Medical Director and PI before M2 |
| O7 | MTN and Airtel Uganda USSD screen limits | Not documented for Uganda | Config, defaulting to ≤160 characters. Written values must be on file before Gate A. |
| O8 | Gateway timestamp offset at T1 | Guide §10 | Measured during field testing (M7) and documented before data collection |
| O9 | Alert recipients for red incidents | The Tools say "top 3", the Protocol says "top 3 of each role" | Build to the Protocol: the top 3 of each role |
| O10 | The audit trail's "external daily witness" | Guide §15 adds it; Build Prompt §4 does not mention it | Added: checkpoints are also written through a `Witness` port |
| O11 | Is snake bite its own Tool 1 category? | Guide §5 lists five categories (trauma, obstetric, acute medical, poisoning, other); the triage rule names "poisoning or snake bite" | Placeholder category `poisoning`, labelled "Poisoning or snake bite", is the one time-critical category until Tool 1 is received (ADR-0019) |

## 4. Architecture

### 4.1 Style

The system is a modular monolith built with hexagonal (ports and adapters) architecture. It is one Go module, compiled into one binary per process. Every process is stateless behind HAProxy. **Channels only translate, the core decides, and the event log is the one source every downstream component reads** (Build Prompt §7).

Dependency rule:

```
cmd/* (composition roots)  ──►  internal/<context>  ◄──  internal/platform/* (adapters)
                                     │
                                     └── pure domain + application services; ports are Go
                                         interfaces declared by the consuming context
```

- A context never imports `internal/platform`. Adapters import contexts so that they can implement the contexts' ports.
- A context imports another context only through that context's root package. Its internals sit under a nested `internal/` that the Go compiler protects.
- `tools/archcheck` (built on `go list -deps -json`, with no dependencies) enforces a declared allowlist of context-to-context imports. It also checks two further rules:
  - only `cmd/pseudonymiser` imports both `platform/pg/operational` and `platform/pg/analytic`;
  - only `internal/platform/**` imports `net`, `net/http` (outside `httptest`), or database drivers.

### 4.2 Processes (`cmd/`)

| Process | Responsibility | Stores it holds credentials for |
|---|---|---|
| `intake-gw` | USSD callbacks from both routes, sessions, the Tool 1 state machine, triage, the responder USSD menu | operational (role `op_intake`), Valkey |
| `ivr-controller` | Asterisk ARI: IVR, outbound alert calls, masked bridge, arrival-confirmation and health-centre calls. Records nothing. | operational (`op_ivr`) |
| `dispatch-worker` | DBOS workflows: ranking, rings, alert ladder, stand-downs, forwarding, escalation, follow-up timers | operational (`op_dispatch`) |
| `console-api` | Connect RPC v2 with server streaming for the live board | operational (`op_console`) |
| `payments` | MTN MoMo and Airtel Money payouts, retries, nightly reconciliation | operational (`op_payments`) |
| `pseudonymiser` | One-way export of closed incidents. Writes ID pairs to the linkage vault only. | operational (read), analytic (write), linkage (insert) |
| `retention-agent` | Key periods, holds, crypto-shredding, the destruction log | operational, media (key operations only) |
| `outbox-relay` **(added)** | Relays outbox rows into the audit ledger and signs a checkpoint every 5 min. It is the only process with audit credentials. | operational (`op_relay`), audit (`audit_appender`) |
| `sautictl` **(added)** | Operator CLI: `migrate`, `verify-ledger`, `verify-projections`, `checkpoint`, `freeze-zones`, `dev-keygen` | per subcommand, least privilege |
| `app-api` | Phase 2, flagged off | — |

### 4.3 Bounded contexts (`internal/`)

| Context | Owns |
|---|---|
| `intake` *(added)* | Tool 1 session state machine, screen catalogue (English and Luganda), per-operator character limits, resume |
| `incident` | Incident aggregate, event types, the pure reducer `Fold(events) → State`, the Damm reference, the 10 commands |
| `triage` | CEL decision-table engine (`cel.dev/cel-go`), logic versions and fingerprints |
| `location` | UTM 36N 500 m grid (EPSG:32636), Chebyshev rings, gazetteer port, fuzzy-match port (`pg_trgm`), operational zone set and its freeze |
| `responder` | Registry, the three gates (certified, insured, kit OK), availability |
| `dispatch` | Ranking, rings, alert ladder planner, forwarding planner, decline reasons |
| `field` | Field status, arrival verification, arrival grades and evidence flags |
| `payment` | Payout state machine, provider response taxonomy (including *unknown*), reconciliation, cost ledger |
| `consent`, `followup`, `dsr` | Consent states (Guide §14), Tool 4 attempts, data-subject request clocks |
| `safety` *(added)* | Reportable events register, rolling window over red incidents, stopping rules, dispatch suspension |
| `audit` | Ledger entry model, canonical encoding, chain verification, checkpoints |
| `crypto` | Envelope encryption, key periods, keyed phone hash, `KeyProvider`/`Signer`/`Witness` ports |
| `config` *(added)* | Versioned configuration, the amendment gate, feature flags |
| `platform/*` | Adapters: `pg/{operational,analytic,linkage,audit,media}`, `valkey`, `otel`, `logging` (redaction), `oidc`, `africastalking`, `ari`, `momo`, `airtel`, `openbao`, `devkeys`, `egress`, `clock` |

### 4.4 Commands

Every channel adapter translates its input into one of 10 commands: `StartSession`, `AnswerScreen`, `AcceptAlert`, `DeclineAlert`, `ReportArrived`, `ReportDeparted`, `ReportAtFacility`, `SubmitForm2`, `SetAvailability`, `RequestBridge`. These are the only entry points into the domain. Because of this, a caller can start on USSD and finish on IVR, and a responder can accept on USSD and report by voice, with no special handling.

## 5. Repository layout

```
api/proto/sauti/v1/        Connect contracts (buf v2)
cmd/                       one binary per process (§4.2)
internal/                  bounded contexts + platform adapters (§4.3)
db/<store>/migrations/     forward-only SQL, one directory per store
db/<store>/queries/        sqlc query files
config/                    versioned dispatch params, SMS templates, decline reasons, flags (gated)
triage-rules/              signed decision tables + clinical scenario tests (gated)
web/console/               React 19.3 SPA (M5)
telephony/                 Asterisk dialplan, prompt catalogue (M1)
analytics/                 enclave notebooks, disclosure control, metric SQL (M6)
deploy/                    Ansible, Quadlet, HAProxy, Patroni (P0)
tests/{sim,e2e,load,drills}
tools/{archcheck,amendgate,freshness,screenlen}
scripts/ci/                CI logic; workflows are thin wrappers
docs/{tutorials,how-to,runbooks,reference,explanation,adr}/
governance/                ai_risk_manifest.yaml, amendments.yaml, compliance_check.py
threat-model/              threats.yaml, validate_threats.py
```

## 6. Data architecture

### 6.1 Stores and roles

There are five PostgreSQL 18 databases. Each has an owner role used only for migrations and a set of least-privilege runtime roles, and no grant crosses databases. In development and CI the five share one server. In production, analytic and linkage get separate clusters and network segments (MVP doc, "Data architecture").

| Store | Runtime roles | Notes |
|---|---|---|
| operational | `op_intake`, `op_ivr`, `op_dispatch`, `op_console`, `op_payments`, `op_relay`, `op_pseudonymiser`, `op_retention` | Holds the DBOS system schema `dbos` |
| audit | `audit_appender` (EXECUTE on `audit.append` only), `audit_reader` | No runtime role has UPDATE or DELETE. Only the migration owner could alter rows, and checkpoints make that detectable (§12.2 tamper test). |
| analytic | `an_writer` (pseudonymiser), `an_reader` | CODEOWNERS: PI |
| linkage | `lk_writer` (insert only), `lk_reader` (PI and delegate, through a function that requires a reason) | Every read is logged to audit |
| media | `md_writer`, `md_reader` | Consented interview audio only |

### 6.2 Single write path (operational)

- Application roles have **no** INSERT, UPDATE or DELETE on `incident_events`, `incidents`, `outbox` or consent rows. Three `SECURITY DEFINER` functions are the only way in:
  - `op.open_incident(...)` creates the incident, its reference and the `SessionStarted` (T1) event in one transaction;
  - `op.append_incident_event(...)` handles every later change (below);
  - `op.mark_outbox_delivered(ids)` is the only outbox mutation, and only `op_relay` may execute it.
- All later writes go through the `SECURITY DEFINER` function `op.append_incident_event(p_incident, p_expected_version, p_type, p_payload, p_actor, p_idem_key)`. In one transaction it:
  1. returns the existing event if `(incident_id, idem_key)` is already present (idempotency);
  2. checks `expected_version` (optimistic concurrency);
  3. checks the transition against `op.incident_transitions`;
  4. inserts the event with `recorded_at = clock_timestamp()`, so the database sets the T-time;
  5. updates the `incidents` projection (state, version, t1..t7);
  6. inserts an `outbox` row.
- The pure Go reducer is the logic of record. `sautictl verify-projections` replays the events and fails on any drift. CI runs it, and so does every restore drill.
- Primary keys use `uuidv7()`. Every timestamp is `timestamptz` in UTC.
- `incident_events` and the other high-volume tables are partitioned by month, so a retention period maps to whole partitions and one key.

### 6.3 Outbox → audit ledger

- `outbox-relay` claims rows in batches with `FOR UPDATE SKIP LOCKED` and calls `audit.append(...)`. Appends are idempotent on `entry_id`, which equals the outbox row ID, so delivery is at-least-once and safe.
- If the audit store is down, the outbox grows and dispatch is unaffected (hard constraint 14). The metric `sauti_outbox_lag_seconds` raises an alert above 60 s.
- Audit entries carry **only** the event type, incident ID, staff ID, logic version and hashes (hard constraint 8). Three layers enforce this:
  - the SQL write function builds the outbox payload from typed columns and includes only the **SHA-256 of the event payload**, never its content;
  - the relay decodes that payload strictly, so an unknown key stops the relay (`ErrPoisonRow`);
  - `audit.entries` has typed columns only (`staff_id uuid`, no free-form column), so there is nowhere to put free-form data.
- `audit.append` takes `pg_advisory_xact_lock(<chain>)` as a separate statement, assigns `seq` (`bigint`), and computes `hash = sha256(prev_hash ‖ canonical(entry))` in SQL. The canonical encoding is specified in `docs/reference/audit-canonical-encoding.md`, and an independent Go implementation in `internal/audit` re-computes it to verify.
- **Checkpoints.** Every 5 minutes, a body modelled on C2SP tlog-checkpoint (`origin`, `seq`, `head_hash`, `time`) is signed with **ML-DSA-65** (Go 1.27 `crypto/mldsa`). The signed checkpoint is stored in `audit.checkpoints` and also written through the `Witness` port: a write-once directory in development, the backup snapshot path in production, plus a daily external witness (O10).
- `sautictl verify-ledger` checks the chain, the sequence continuity and every checkpoint signature against the published public key.

### 6.4 Crypto and retention

- **Identifiers.** Each identifier is stored as `(ciphertext, key_id)`. The cipher is AES-256-GCM, and the AAD binds the ciphertext to its store, table, column and row ID, so ciphertexts cannot be swapped between rows.
- **Redial lookup.** `phone_hmac` is HMAC-SHA-256 under the current period key. A lookup checks the current and the previous key period.
- **Ports and adapters.**
  - `KeyProvider`: an OpenBao adapter (2.7 transit `datakey`, from P0) and a `devkeys` file adapter that **refuses to start when `SAUTI_ENV=production`**.
  - `Signer`: ML-DSA-65, with the same two adapters.
  - `Witness`: covered in §6.3.
- **Crypto-shredding.** Each record class and period has its own key. Destroying a key needs OpenBao 2.7 control-group approval from the technical operator and the DPO. The PI signs destruction of the study stores (RMRP §5). Built in M6.

### 6.5 Migrations

`internal/platform/pg/migrate` uses only the standard library and `pgx`:
- Migrations are embedded per store with `embed.FS`.
- They are forward-only, with no down migrations: problems are fixed forward.
- The sha256 checksum of each file is stored in `schema_migrations`. If an applied file has changed, the migrator refuses to run.
- It runs under a session advisory lock, one transaction per file.

### 6.6 PostgreSQL 18 and sqlc

sqlc v1.31.1 (the latest release) parses PostgreSQL 17 grammar only. PG18 grammar (`WITHOUT OVERLAPS`, `RETURNING OLD/NEW`) is supported only on unreleased sqlc main.

**Spike result (2026-11-12).** The static release binary (sha256 `497ae4fc…27354`) parses `DEFAULT uuidv7()`, partitioned tables and plpgsql `SECURITY DEFINER` functions. It fails only on `WITHOUT OVERLAPS`.

**Decision.** The few PG18-only constraints are added inside `DO $$ BEGIN EXECUTE '…'; END $$` blocks, which sqlc does not parse, and each such block is commented with a pointer to ADR-0011. Because sqlc cannot be built here without cgo, the pinned release binary, checksum-verified by `scripts/tools/install-sqlc.sh`, is used both locally and in CI.

## 7. Robustness mechanisms

1. **Functional core, imperative shell.** The alert ladder, rings, forwarding, retries and follow-up windows are pure planners: `Plan(state, now) → (actions, next_wake)`. DBOS (v1.8) executes the plans, and its `Recv` uses pointer types so that a timeout (which DBOS returns as a zero value) is distinguishable from a real message. Planners are tested with table tests and `testing/synctest`.
2. **Fail safe to a human.** Every automated step has a deadline. An error or timeout goes to the `Escalate` port, which places a coordinator voice call through a durable, retried workflow. A triage error makes the incident red and alerts a coordinator.
3. **Idempotency.**
   - USSD steps key on `sessionId`+`text`, and voice on `callId`+step.
   - Payouts key on `incident:responder:kind`. The provider reference (a UUIDv4 for MoMo) is persisted **before** the first call. A 409 or timeout means "query status by reference", never "re-send".
   - Outbox entries key on `entry_id`, and DBOS workflow IDs are derived from incident, role and ring.
4. **Concurrency.**
   - First acceptance wins, enforced by a partial unique index on active assignments per `(incident, role)`.
   - Events use optimistic version checks.
   - Ledger appends are serialised by an advisory lock.
5. **Bulkheads and timeouts.**
   - Each store gets its own connection pool.
   - The USSD handler has 800 ms in total. No external call happens in the USSD request path.
   - Each provider has an in-house circuit breaker with failover to its second route.
6. **Dispatch-only readiness.** `/readyz` checks only what is on the dispatch path. Analytics, payments or monitoring being down never fails readiness.
7. **No PII in telemetry.**
   - `Phone` and `PersonName` types implement `slog.LogValuer` and render masked.
   - The OpenTelemetry collector masks a second time.
   - An end-to-end test scans every log line and span for phone-number patterns.
8. **Egress allowlist.** All outbound HTTP goes through `platform/egress`, which enforces a host allowlist; a test fails on any other host. In production, nftables enforces default-deny as well.
9. **Time.**
   - The database sets every T-time except the attested T5.
   - The health check alarms when the chrony offset exceeds 10 ms.
   - The gateway offset at T1 is measured in M7 (O8).
10. **Verifiers.** `verify-ledger` and `verify-projections` run nightly and in every restore drill.
11. **Fuzzing.** Africa's Talking form parsing, `text` accumulation, the incident-reference parser and (M1) CEL inputs.
12. **Africa's Talking callback authentication.** AT does not sign callbacks, so the adapter uses three checks: a secret path token, a `serviceCode` check, and a source-IP allowlist (requested from AT). TLS 1.2 is accepted only on this callback (Build Prompt §13).

## 8. Security controls

- **Verification standard.** OWASP ASVS 5.0 (2025-05-30) at Level 2, and Level 3 for authentication and the linkage vault.
- **M0 touches these ASVS chapters:**
  - V11 Cryptography: envelope encryption, ML-DSA checkpoints;
  - V14 Data Protection: store separation, no PII in logs;
  - V15 Secure Coding & Architecture: boundaries, dependency policy;
  - V16 Security Logging: tamper-evident ledger;
  - V13 Configuration: no secrets in Git, `devkeys` refuses production.
- **In transit.** TLS 1.3 with mTLS between services and X25519MLKEM768 hybrid key exchange. Arrives with the P0 adapters.
- **Hard-constraint compliance tests (Build Prompt §16).** M0 builds the harness and the first checks:
  - import boundaries;
  - outbound host allowlist;
  - no caller number in logs;
  - frozen config without an amendment record.

  The SMS-content and screen-length checks arrive in M1 and M2.

## 9. CI and supply chain

- **Pipeline.** CI logic lives in `scripts/ci/*.sh` (bash, `set -euo pipefail`). `.github/workflows/ci.yml` is a thin wrapper, and every `uses:` is pinned by full commit SHA. This follows the trivy-action tag-hijack of 2026-03-19 (CVE-2026-33634). At P0, Forgejo Actions calls the same scripts.
- **Jobs:**
  - `fmt` and `lint`: gofmt, `go vet`, golangci-lint v2 with gosec;
  - `archcheck`;
  - `unit`: `-race -shuffle=on`;
  - `fuzz-smoke`;
  - `integration`: Testcontainers on PostgreSQL 18;
  - `vuln`: govulncheck, OSV-Scanner v2 with the licence allowlist MIT, BSD, Apache-2.0, MPL-2.0 and ISC;
  - `secrets`: gitleaks;
  - `sbom`: cyclonedx-gomod, CycloneDX 1.6 / ECMA-424 (1.7 exists and is noted);
  - `provenance`: `actions/attest@v4`, on release;
  - `governance`: amendgate, compliance_check, validate_threats, freshness, ADR index.
- **Toolchain.** `go.mod` declares `go 1.27` and `toolchain go1.27.4`, and build tools are pinned through the `tool` directive. Images are pinned by digest from P0.
- **Dependencies.** About twenty runtime dependencies. Each new one needs a PR note covering why, licence, maintainer health and fallback (`docs/reference/dependencies.md`).
- **Test environments.**
  - Local machines without a container runtime use `embedded-postgres` v2 (PostgreSQL 18.6).
  - In M0, CI uses an official `postgres:18.6` service container, pinned by digest and passed in through `SAUTI_TEST_PG_DSN`.
  - Testcontainers v0.45 (which supports Podman) arrives in M1 for Valkey and for failover tests that must kill a database mid-test (ADR-0012).

## 10. Governance and living documentation

This maps the global engineering rules onto this repository:

| Global rule | This repository |
|---|---|
| Diátaxis | `docs/tutorials`, `docs/how-to` + `docs/runbooks`, `docs/reference` (config, env, events, schemas, API), `docs/explanation` (architecture) |
| MADR 4.0 ADRs | `docs/adr/ADR-NNNN-*.md` with Context and Problem Statement, Decision Outcome, Consequences and Confirmation; indexed in `docs/adr/README.md` |
| `docs/AGENT_ARCHITECTURE.md`, `docs/RAG_ARCHITECTURE.md` | Not applicable: there is no RAG and no LLM agent. The equivalent is `docs/explanation/architecture.md`. |
| `docs/MODEL_CARD.md` | The deterministic triage decision table is treated as the "model" (versions, scenarios, limits, the "no model may lower a severity" rule). Sunbird `asr-whisper-large-v3-salt` (Luganda WER 14.2%) is listed as flagged off. |
| `governance/ai_risk_manifest.yaml` | Risk register in the shape of ISO/IEC 42001 and NIST AI RMF, with a `verified_by` test pointer per risk and an `aibom_inventory` (CycloneDX) |
| `governance/compliance_check.py` | Checks that every risk has a `verified_by` pointing to a test that exists, and that the AIBOM lists every model and provider in config |
| `threat-model/validate_threats.py` | STRIDE and LINDDUN threats in `threats.yaml`, each with mitigations and test pointers |
| `python3 -m app.freshness --check` | `python3 -m tools.freshness --check`: code paths cited in docs exist, the ADR index is complete, and the traceability matrix has no dangling test IDs |
| Traceability | `docs/traceability.md`: each of the 15 MVP policy requirements, 14 hard constraints and 17 scenarios mapped to a test or drill |

- **CodeRabbit.** `.coderabbit.yaml` uses the `assertive` profile, with path instructions for `triage-rules/`, `db/**`, `internal/audit`, `internal/crypto` and `config/`.
- **CODEOWNERS.** Every entry is `@mpairwe7` for now, with comments naming the role each path is meant for (emergency-medicine co-investigator, PI). This is revisited when the role holders have accounts.
- **Merging.** Nothing merges without the user's explicit go-ahead, after all CI and CodeRabbit gates have passed.

## 11. Milestone roadmap (architecture level)

Each milestone gets its own plan, demo and review stop (Build Prompt §0, §18).

| # | Builds | Demo | Needs first |
|---|---|---|---|
| **M0** | §12 below | A fixture incident flows through the event log and the ledger | — |
| M1 | Tool 1 state machine, IVR through `ivr-controller`, resume (10 min), dedup, grid and gazetteer, CEL triage, screen-length gate | Scenarios 3–6 | **Tool 1 wording and category list**, MTN/Airtel limits (O7) |
| M2 | Ranking, rings, alert ladder, decline reasons, forwarding, bridge, suspension control | Scenarios 1, 2, 7–11, 14 | O2, O6 |
| M3 | Field status, arrival verification and grades, Tool 2, slips | Scenario 12 | **Tool 2 action codes** |
| M4 | Payments, idempotency, reconciliation, cost ledger | Scenario 13 | Payment amounts (executive team) |
| M5 | Console API and React console | Scenario 17, Playwright suite | **Tool 4 scripts** |
| M6 | Pseudonymiser, analytic store, metric SQL, Grafana via Git Sync, retention and shredding | Fixture numbers match; scenario 16 | — |
| M7 | Second USSD route and SIP carrier, synthetic monitors, switchover, k6, penetration test, field test | Scenario 15 | Real handsets, P0 |
| P0 | Infrastructure (Proxmox, Patroni, OpenBao with HSM, Keycloak, Harbor, Forgejo, observability) | Failover and restore drills | Hardware at Raxio and Site B |

## 12. M0 specification

### 12.1 Deliverables

1. **Module.** `sautihealth.com/sauti` with `go 1.27` / `toolchain go1.27.4`. The repository skeleton follows §5.
2. **`internal/incident`.**
   - Event types (`SessionStarted`, `ScreenAnswered`, `IncidentClassified`, …) and states.
   - The pure reducer.
   - The 10 command types.
   - `Reference`: 9 digits from `crypto/rand` plus a Damm check digit, formatted `123 456 7890`. `Parse` validates the check digit. Uniqueness comes from a database unique index, with retry on collision.
3. **`internal/audit`.** Entry model, canonical encoding, chain verifier, checkpoint body plus ML-DSA-65 sign and verify.
4. **`internal/crypto`.** AES-256-GCM envelope with row-bound AAD, HMAC phone lookup, the `KeyProvider`/`Signer`/`Witness` ports, and the `devkeys` adapter (which refuses production).
5. **`internal/config`.** Versioned config loader with a content fingerprint, the `study_window_active` flag and feature flags (all section 11 flags off).
6. **`internal/platform/pg/*`.**
   - The migrator.
   - Per-store connection pools.
   - The operational repository, calling `op.append_incident_event`.
   - The outbox claimer.
   - The audit appender.
7. **Baseline migrations for all five stores:**
   - **operational:** `incidents`, `incident_events` (partitioned), `incident_transitions`, `sessions`, `outbox`, `config_versions`, `key_periods`, the three write functions (§6.2) and the roles.
   - **audit:** `entries`, `checkpoints`, `append()`, the roles.
   - **analytic:** `meds_records` with the 10 MEDS fields (Guide §10) plus `age_band`, `sex`, `logic_version`, `zone_set_version`, `arrival_grade`, `evidence_flags` and `consent_status`; `suppression_ledger`.
   - **linkage:** `pairs`, `access_log`, and a read function that requires one of the four allowed reasons.
   - **media:** `recordings`, `chunks`.
8. **`cmd/outbox-relay`.** Relay loop, a 5-minute checkpoint ticker, `/livez` and `/readyz`, and Prometheus metrics.
9. **`cmd/sautictl`.** `migrate --store`, `verify-ledger`, `verify-projections`, `checkpoint`, `dev-keygen`.
10. **`cmd/intake-gw` skeleton.**
    - Health endpoints.
    - The Africa's Talking USSD adapter (form parsing, path-token authentication, `CON`/`END` rendering). In M0 it handles `StartSession` only: it creates the incident, its reference and the T1 event, then replies `END` with the incident number and a "PROTOCOL-PENDING" notice.
    - Idempotency on `sessionId`+`text`.
11. **`platform/logging`.** slog redaction handler with masked PII types.
12. **`platform/egress`.** HTTP client with a host allowlist.
13. **`tests/sim`.** The Africa's Talking USSD simulator (session IDs, `text` accumulation, retries). The ARI event fake and the SMS and USSD-push sinks arrive in M1 and M2, together with the ports they implement (YAGNI).
14. **`tools/`.** `archcheck`, `amendgate`, `freshness`.
15. **CI and governance.** `scripts/ci/*`, `ci.yml`, `.coderabbit.yaml`, CODEOWNERS, `governance/*`, `threat-model/*`.
16. **Docs.** ADR-0001…0010, the Diátaxis skeleton, architecture explanation, `traceability.md`, `MODEL_CARD.md`, `.env.example`, the M0 demo runbook.

### 12.2 Demo (`tests/e2e/m0_fixture_test.go`, also `sautictl demo m0`)

1. The simulator dials `*XXX#`, and `intake-gw` creates the incident. The reference has a valid Damm digit, and T1 is set by the database.
2. A fixture command sequence appends events (screens answered, classified red, T2).
3. `outbox-relay` delivers every event to the audit ledger. Each entry contains no phone number or name.
4. A checkpoint is signed with ML-DSA-65 and written to the witness directory.
5. `verify-ledger` and `verify-projections` both pass.
6. **Tamper test:** the owner role edits one ledger row, and `verify-ledger` fails at that row's `seq`.
7. **Retry test:** a re-sent USSD callback returns the identical screen, and no second event is written.

### 12.3 Test plan

- **Unit tests:**
  - Damm against the Wikipedia vectors (572 → 4) plus exhaustive single-digit-error and adjacent-transposition detection;
  - reducer invariants;
  - canonical-encoding golden files;
  - AAD swap rejected;
  - `devkeys` refuses production;
  - config fingerprint is stable;
  - amendgate pass and fail fixtures.
- **Fuzz tests:** AT form parser, `text` splitter, reference `Parse`.
- **Integration tests (PostgreSQL 18):**
  - every store migrates from zero;
  - checksum tamper is refused;
  - privilege matrix, e.g. `op_intake` cannot `UPDATE incident_events`, and `audit_appender` cannot `DELETE`;
  - append idempotency and version conflict;
  - illegal transition rejected;
  - 50 concurrent appenders produce a valid chain;
  - the relay survives an outage of the audit database and catches up.
- **Compliance tests:** archcheck, egress allowlist, no phone patterns in captured logs.
- **Acceptance:**
  - all of the above green in CI;
  - the demo passes locally (embedded-postgres) and in CI (Testcontainers);
  - governance gates green;
  - zero unresolved CodeRabbit findings.

### 12.4 Out of scope for M0

Out of scope: the Tool 1 screens and triage, dispatch, IVR flows, the console, payments, the pseudonymiser, real OpenBao and Keycloak adapters, and `deploy/`.

## 13. Risks (new from research, 2026-11-12)

| Risk | Impact | Mitigation |
|---|---|---|
| No documented AT network-initiated USSD API (O2) | Decision 8's first rung may be unavailable | `UssdPush` port. SMS plus voice ladder. Ask Ops to get written confirmation. |
| sqlc has no released PG18 parser | Code generation fails on PG18 DDL | M0 spike (§6.6) |
| AT callbacks are unsigned | Forged intake | Path token, `serviceCode` check, IP allowlist |
| 2G spectrum refarming (Guide §4) | Long-term reach | On the risk register. Track operator roadmaps. |
| PostgreSQL 19 GA (19.11.2026) before Patroni supports it | Accidental upgrade | Stay on 18 until Patroni releases PG19 support (Build Prompt §4) |
| Asterisk 24 LTS released 2026-11-05 | — | Stay on 22 LTS (fully supported until 2028-10-16) as pinned. Revisit after the pilot. |
| CyCoreSystems/ari has no valid v6 tag | Supply chain | Pin pseudo-version `v6.0.0-20260805125548-90f525536787` with a dependency note (M1) |

## 14. Verified versions (checked 2026-11-12)

| Component | Version | Note |
|---|---|---|
| Go | 1.27.4 | stdlib `uuid`, `crypto/mldsa`, json/v2 |
| connect-go | `connectrpc.com/connect/v2` v2.0.2 | buf 1.75.0, protoc-gen-go 1.36.12 |
| pgx | v5.13.0 | ≥5.12.0 required (two GHSAs) |
| sqlc | v1.31.1 | PG17 parser, see §6.6 |
| DBOS Transact Go | v1.8.0 | `Recv` returns a zero value on timeout |
| cel-go | `cel.dev/cel-go` v0.33.2 | module path moved |
| valkey-go | v1.0.79 | Valkey 9.1.3, Sentinel supported |
| OpenBao | 2.7.2, `openbao/api/v2` | control groups in 2.7; PKCS#11 seal is an external plugin |
| PostgreSQL / Patroni | 18.6 / 4.1.5 | |
| testcontainers-go / embedded-postgres | v0.45.0 / v2.0.1 (PG 18.6) | |
| golangci-lint / govulncheck / osv-scanner | v2.15.0 / v1.9.0 / v2.7.1 | |
| cosign / cyclonedx-gomod / gitleaks | v3.1.4 / v1.12.0 / latest release, verified and pinned by SHA in the M0 plan's CI task | |
| Asterisk | 22 LTS | 24 LTS also available |
| React / TypeScript | 19.3.1 / 7.0.2 with 6.0.2 alongside | typescript-eslint still needs TS 6 |
| Vite / Vitest / Playwright | 8.3.8 / 4.1.12 (pinned line; 5.1 exists) / 1.65.0 | |
| pnpm | 12.x | `minimumReleaseAge: 10080`, `allowBuilds`, settings in `pnpm-workspace.yaml` |
| Forgejo | 15 LTS (15.0.11, to 2027-07-15) | |

Sources are listed in `docs/reference/dependencies.md` (written in M0).
