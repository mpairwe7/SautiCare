# Sauti Dispatch: Phased Delivery Roadmap

> **For agentic workers:** This is the master roadmap. Each phase is implemented from **its own detailed plan** (`docs/superpowers/plans/<date>-<phase>.md`), written when the phase starts against the code that exists by then. REQUIRED SUB-SKILL for every phase plan: superpowers:subagent-driven-development or superpowers:executing-plans.

**Goal:** Deliver every feature in Build Prompt v0.2 in reviewable phases. Each phase ships working, tested software together with its documentation.

**Architecture:** A hexagonal modular monolith in Go 1.27. PostgreSQL 18 holds five separate stores. Every write goes through a single path enforced by the database. A transactional outbox feeds a hash-chained audit ledger with ML-DSA-signed checkpoints, and DBOS runs the durable workflows (spec §4–§7).

**Spec:** `docs/superpowers/specs/2026-11-12-sauti-dispatch-design.md`

## Global constraints (apply to every phase)

- The 14 hard constraints in Build Prompt §3. A phase that violates one does not merge.
- Technology comes from spec §14. A new dependency needs a dependency note in `docs/reference/dependencies.md` covering why, licence (MIT, BSD, Apache-2.0, MPL-2.0 or ISC), maintainer health and fallback.
- Clinical content (Tool 1 wording and categories, Tool 2 codes, Tool 4 scripts) is **never invented**. Until the source documents arrive it is a placeholder marked `protocol_status: pending`, and a binary refuses to start with `SAUTI_ENV=production` while any placeholder remains.
- Open decisions use their safe default and are flagged (spec §3).
- Changes to gated paths need an `amendment_approved` record once `study_window_active=true`.

## Documentation definition of done (every phase)

Every phase updates the following in the same PR:

1. **ADRs** in `docs/adr/` (MADR 4.0): one per architectural, protocol-affecting, security or dependency decision, indexed in `docs/adr/README.md`.
2. **Runbooks** in `docs/runbooks/`: one per new operational procedure or alert, using the template in `docs/runbooks/_template.md`. The template covers summary, triggers, impact, diagnosis, mitigation, escalation, verification, last drill date and owner.
3. **Reference** in `docs/reference/`: config keys, environment variables (`.env.example`), event catalogue, store schemas, API, dependencies.
4. **Explanation** in `docs/explanation/`: architecture, with the C4 diagrams updated.
5. **Traceability** in `docs/traceability.md`: each requirement, hard constraint and scenario mapped to the test or drill that proves it.
6. **AI governance**: `docs/MODEL_CARD.md`, `governance/ai_risk_manifest.yaml` (`verified_by` and `aibom_inventory`), `threat-model/threats.yaml`.
7. **Gates**: `python3 -m tools.freshness --check`, `python3 governance/compliance_check.py` and `python3 threat-model/validate_threats.py` all pass.

## Phases

| Phase | Milestone | Branch | Delivers | Exit demo | External inputs |
|---|---|---|---|---|---|
| 1 | **M0 Foundation** | `m0-foundation` | Module, five-store migrations, single write path, outbox, audit ledger with ML-DSA checkpoints, crypto, config and amendment gate, USSD StartSession, simulators, CI, governance, docs | Spec §12.2 (dial → ledger → verify, plus tamper and retry tests) | — |
| 2 | **M1 Intake and triage** | `m1-intake` | Tool 1 USSD state machine (8 screens), screen catalogue in English and Luganda with the length gate, Valkey session cache, resume (10 min), dedup and merge/split, out-of-area flow, IVR through `ivr-controller` (ARI adapter and fake), UTM 36N grid, gazetteer with `pg_trgm`, CEL triage with signed rule bundles, logic fingerprint, rule-error → red | Scenarios 3–6 | Tool 1 wording (placeholders until then) |
| 3 | **M2 Dispatch** | `m2-dispatch` | Responder registry and the three gates, availability (WITHOUT OVERLAPS, 8 h expiry with SMS reminder), ranking (decision 4 default), rings, alert ladder (USSD push port, SMS, voice at 20 s, USSD pull menu), decline reasons, red parallel top 3 per role, first accept wins, stand-downs, health-centre forwarding, masked bridge, escalation port, suspension control, surge queue; DBOS workflows | Scenarios 1, 2, 7–11, 14 | O2, O6 |
| 4 | **M3 Field and verification** | `m3-field` | Responder "My incident" menu, T5–T7, cannot-find and unsafe-withdrawal flows, Tool 2 form, outbreak escalation, caller confirmation call, slip corroboration, plausibility bounds, variance monitor, arrival grades and evidence flags | Scenario 12 | Tool 2 codes |
| 5 | **M4 Payments** | `m4-payments` | Payout state machine, MoMo and Airtel adapters, references persisted before the first call, retries at 5, 30 and 60 min then the manual queue, status-by-reference, wallet-change hold, nightly reconciliation, training and response kinds, cost ledger | Scenario 13 | Payment amounts |
| 6 | **M5 Console** | `m5-console` | Connect v2 console API with streams, OIDC middleware, audited reveal, React 19.3 console (live board with MapLibre/PMTiles, tasks, logs, study, responder history), follow-up (Tool 4) with consent-first ordering, DSR and breach workflows, Playwright and axe suite | Scenario 17 + Playwright | Tool 4 scripts |
| 7 | **M6 Research data and retention** | `m6-research` | Pseudonymiser, consent-gated export, linkage vault, suppression ledger with restore replay, metric SQL and fixtures, Grafana dashboards in Git, disclosure control, FHIR R4 shaping (no exchange), retention agent and crypto-shredding | Fixture metrics match; scenario 16 | — |
| 8 | **M7 Resilience and readiness** | `m7-resilience` | Second USSD route (UCC code) adapter, second SIP carrier config, synthetic monitors, k6 load scripts (200 sessions, 50 incidents), failover and switchover drill scripts, degradation table tests, go-live checklist | Scenario 15 (simulated); k6 scripts | Hardware and real handsets for the real drills |
| 9 | **P0 Infrastructure as code** | `p0-infra` | Ansible roles and Quadlet units for every process, HAProxy and keepalived, Patroni and etcd, Valkey Sentinel, OpenBao (transit, PKI, control groups) and Keycloak config, nftables, WireGuard, chrony NTS, pgBackRest and ZFS, observability stack, Wazuh and Falco, Harbor and cosign policy, Forgejo workflows | Lint and syntax checks; drill runbooks | Hardware at Raxio and Site B |
| 10 | **P2 Flagged enhancements** | `p2-flagged` | `app-api`, caller tracking page (under 100 KB), speech-assist port and in-country Sunbird client, Android responder app (Kotlin), all flags off | Flag-off tests; app builds if the toolchain allows | Ethics amendment before any use |

## What cannot be proven in this environment

Each phase states these honestly in its PR:
- real-hardware failover and restore drills (P0, M7);
- live telco, MoMo and Airtel sandboxes, which need provider credentials (simulators and contract tests stand in);
- field tests on real handsets;
- the penetration test;
- a Kotlin build if the Android SDK is incomplete.

## PR flow per phase

1. Work on the phase branch, which is stacked on the previous phase until that one merges.
2. Run the full local verification, then push and open a PR.
3. Poll `gh pr checks` until every check passes.
4. Resolve every CodeRabbit finding.
5. **Ask the user before merging.**
