# Sauti Dispatch

Emergency dispatch for six wards of Kawempe Division, Kampala. A person reports a medical emergency from **any phone**: a basic 2G handset, with no data and no airtime. Sauti then:

1. classifies it red, yellow or green with deterministic rules;
2. alerts the nearest trained Village Health Team member and community transporter;
3. tracks the incident to the health facility;
4. pays the responders by mobile money;
5. records the timestamps T1–T7 that the pilot study analyses.

All intelligence sits on the server, and all personal data stays in Uganda.

> **Status: M0 Foundation.** This milestone delivers the platform foundation, not a live service: five-store data architecture, the single write path, a tamper-evident audit ledger, crypto, configuration with the amendment gate, USSD intake of the first screen, CI and governance. Clinical content (Tool 1 wording, Tool 2 codes, Tool 4 scripts) is *pending*. The processes refuse to start in production until it is approved and the P0 key infrastructure exists. See the [phased roadmap](docs/superpowers/plans/2026-11-12-phased-roadmap.md).

## Quick start

```bash
scripts/ci/unit.sh                                         # unit tests
go test -tags integration -count=1 -v ./tests/e2e/         # M0 demo on embedded PostgreSQL 18.6
```

See [the local development tutorial](docs/tutorials/local-development.md) for the gates CI runs.

## Layout

| Path | Contents |
|---|---|
| `cmd/` | one binary per process: `intake-gw`, `outbox-relay`, `sautictl` (more per milestone) |
| `internal/` | bounded contexts (`incident`, `audit`, `crypto`, `config`, `intake`, `pii`) and adapters (`internal/platform/`) |
| `db/` | forward-only, checksummed SQL migrations and sqlc queries for the five stores |
| `config/` | versioned behavioural configuration; `config/gated/` is frozen during the study window |
| `docs/` | Diátaxis documentation, ADRs, runbooks, traceability, Model Card |
| `governance/`, `threat-model/` | AI risk register and AIBOM, amendment records, STRIDE/LINDDUN threat model and their gates |
| `scripts/ci/` | CI logic shared by GitHub Actions now and Forgejo Actions (P0) |
| `tools/` | `archcheck` (module boundaries), `amendgate` (protocol change control), `freshness` (docs as code) |
| `triage-rules/`, `telephony/`, `tests/`, `build/` | signed triage decision tables; Asterisk dialplan and prompts; end-to-end tests, with load and drill scripts planned for M7; the pinned tool module |
| `api/`, `web/`, `analytics/`, `deploy/` | planned homes with placeholder READMEs: console contracts and app (M5), enclave analytics (M6), infrastructure as code (P0) |

[Repository layout](docs/reference/repository-layout.md) maps every path to the reference architecture, with each planned location's milestone and each deviation's reason.

## Engineering rules

- **Hard constraints.** The 14 hard constraints in the Build Prompt (§3) are non-negotiable, and `docs/traceability.md` maps each one to its proof.
- **Architecture changes.** Every architectural decision is recorded as an ADR in `docs/adr/` in the same pull request.
- **Merging.** Merges require green CI and resolved CodeRabbit findings.
- **Confidential documents.** The governing documents are confidential and are never committed.

## Licence

See `LICENSE`.
