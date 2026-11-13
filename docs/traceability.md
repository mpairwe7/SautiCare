# Traceability matrix

**Type:** Reference (Diátaxis) · **Last updated:** 2026-11-13 (M0) · **Checked by:** `python3 -m tools.freshness --check` (every test pointer must name a test function that exists)

This matrix maps each policy requirement (MVP stack doc, 15 requirements), each of the 14 hard constraints (Build Prompt §3) and each of the 17 scenarios (Build Prompt §16) to the automated test or drill that proves it.

**Status values:**
- **Verified:** M0 fully proves the requirement.
- **Partial:** M0 proves the foundation, and the named milestone completes it.
- **Planned:** a later milestone delivers it.

Policy documents are cited by section only (they are confidential, ADR-0008).

## Policy requirements

| # | Requirement | Source | Status | Proof (M0) | Remaining |
|---|---|---|---|---|---|
| R1 | Personal data and backups stay in Uganda | ISP §4, §5.4, §9 | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesUnlistedHost` | P0: hosting in Uganda, the location of every approved host, nftables, backups. The allowlist test proves only that unlisted hosts are refused, not where listed hosts or backups are. |
| R2 | Operational, analytic and linkage stores kept apart | ISP §4, §5.1–5.2 | Verified | `internal/platform/pg/bootstrap/bootstrap_integration_test.go::TestBootstrapIsolatesStores`<br>`tools/archcheck/check_test.go::TestR4OnlyPseudonymiserJoinsStores` | M6: pseudonymiser |
| R3 | Linkage table only for PI and delegate; access logged with reason | ISP §5.1, §5.5 | Partial | `internal/platform/pg/schemas_integration_test.go::TestLinkageResolveRequiresAllowedReason`<br>`internal/platform/pg/schemas_integration_test.go::TestLinkageRolesAreNarrow` | P0/M5: identities. The tests prove the reason check, the access log and the narrow roles, not that only the PI and the delegate can use those roles. |
| R4 | Incident ID at first contact, no personal data, check character | ISP §5.2 | Verified | `internal/incident/damm_test.go::TestDammDetectsEverySingleDigitError`<br>`internal/platform/pg/opstore/opstore_integration_test.go::TestOpenIncidentSetsT1FromDatabase` | — |
| R5 | Authoritative server-side timestamps | ISP §5.2 | Verified | `internal/platform/pg/opstore/opstore_integration_test.go::TestOpenIncidentSetsT1FromDatabase`<br>`internal/platform/pg/opstore/opstore_integration_test.go::TestProjectionMatchesFold` | P0: chrony NTS + GPS |
| R6 | AES-256 at rest; keys apart from data; TLS in transit | ISP §5.3 | Partial | `internal/crypto/crypto_test.go::TestSealOpenRoundTrip`<br>`internal/crypto/crypto_test.go::TestOpenFailsWithSwappedAAD` | P0: OpenBao/HSM, TLS 1.3 + mTLS |
| R7 | Append-only tamper-evident record of decisions | ISP §5.5 | Verified | `internal/platform/pg/auditstore/auditstore_integration_test.go::TestOwnerUpdateBlockedByTrigger`<br>`internal/platform/pg/auditstore/auditstore_integration_test.go::TestSaveCheckpointRefusesSubMicrosecondSignedAt`<br>`internal/audit/verify_test.go::TestVerifyDetectsBrokenHash`<br>`tests/e2e/m0_fixture_test.go::TestM0FixtureIncident` | — |
| R8 | Unique credentials, RBAC, monthly review, same-day removal | ISP §5.1 | Planned | — | P0/M5: Keycloak, access review report |
| R9 | No credentials before induction and confidentiality undertaking | ISP §5.8, §5.10 | Planned | — | M5/P0: Keycloak gating |
| R10 | No patient information on consumer messaging; voice escalation logged | ISP §5.6 | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesUnlistedHost` | M2: SMS template test; M5: logged voice escalation |
| R11 | Retention schedule, holds, destruction log | RMRP §2–4 | Partial | `internal/platform/pg/schemas_integration_test.go::TestMediaRecordingsRequireDestroyByAndKey` | M6: retention-agent, crypto-shredding |
| R12 | Breach timers 24 h / 48 h / 72 h / 7 days and register | ISP §7 | Planned | — | M5: breach workflow |
| R13 | Data-subject requests in 14 days; withdrawal removes analytic record | ISP §8 | Partial | `internal/platform/pg/schemas_integration_test.go::TestSuppressionLedgerAppendOnly`<br>`internal/platform/pg/schemas_integration_test.go::TestSuppressionDeletesAndBlocksMEDSRecord` | M5/M6: DSR workflow, restore replay |
| R14 | Triage logic versioned; logic version on every record; overrides logged | DPIA risk 8 | Partial | `internal/config/config_test.go::TestFingerprintStableAndSensitive`<br>`internal/incident/fold_test.go::TestApplyRejectsSeverityLowering`<br>`internal/triage/triage_test.go::TestLoadRejectsBadSignature`<br>`internal/triage/triage_test.go::TestLogicVersionAndDevSigned`<br>`tests/e2e/m1_intake_test.go::TestScenario3PoisoningIsYellow` | M5: overrides logged |
| R15 | Pseudonymised outputs with small-cell checks; minimal payment payloads | ISP §5.11 | Partial | `internal/platform/pg/schemas_integration_test.go::TestAnalyticRejectsUnconsentedRecord` | M4: payment payloads; M6: disclosure control |

## Hard constraints (Build Prompt §3)

| # | Constraint | Status | Proof (M0) | Remaining |
|---|---|---|---|---|
| 1 | Data stays in Uganda | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesRedirectToUnlistedHost` | P0: hosting, approved-host locations, network controls, backups |
| 2 | Basic phone first | Partial | `internal/platform/africastalking/ussd_test.go::TestATParsesFormAndRendersCONEND`<br>`internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7`<br>`tests/e2e/m1_intake_test.go::TestScenario5ResumedSessionCompletes` | M1 Part C IVR; M2 responder channels |
| 3 | Deterministic triage and dispatch; no model lowers severity | Partial | `internal/incident/fold_test.go::TestApplyRejectsSeverityLowering`<br>`internal/platform/pg/opstore/opstore_integration_test.go::TestAppendRejectsSeverityLowering`<br>`internal/triage/triage_test.go::TestRuleTableFromBuildPrompt`<br>`internal/triage/triage_test.go::TestLoadRejectsNonTotalTable` | M2 ranking |
| 4 | Server-side time | Verified | `internal/platform/pg/opstore/opstore_integration_test.go::TestOpenIncidentSetsT1FromDatabase`<br>`internal/platform/pg/schemas_integration_test.go::TestIntakeCannotSupplyUSSDStepTimestamp` | M3 attested T5 verification |
| 5 | Never reveal the caller's number | Partial | `internal/pii/pii_test.go::TestPhoneMasksEverywhere`<br>`internal/platform/logging/redact_test.go::TestRedactionMasksPhoneInsideErrorValues`<br>`internal/platform/africastalking/ussd_test.go::TestATLogsAKeyedCorrelationValueNeverTheSessionID`<br>`internal/intake/service_test.go::TestCallbackNumberNeverInPayloadOrSteps`<br>`internal/platform/pg/opstore/tool1_integration_test.go::TestSQLRejectsWhatFoldRejects`<br>`tests/e2e/m1_intake_test.go::TestNoCallerNumberInLogs` | M2 masked bridge; M5 audited reveal |
| 6 | No patient data on messaging apps | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesUnlistedHost` | M2 SMS template content test |
| 7 | No coordinator override outside exceptions | Planned | — | M5 |
| 8 | Audit trail, incident and staff IDs only | Verified | `internal/platform/pg/relay_integration_test.go::TestAuditEntryNeverCarriesPayloadContent`<br>`internal/platform/pg/auditstore/auditstore_integration_test.go::TestSQLHashMatchesGoCanonical`<br>`internal/platform/pg/opstore/opstore_integration_test.go::TestAuditPayloadHashIsSalted`<br>`internal/platform/pg/tamper_integration_test.go::TestOwnerRewriteWithRecomputedChainIsCaughtByWitness`<br>`internal/audit/witness_test.go::TestAnchoredCheckpointerUsesTheLatestWitnessEachTerm`<br>`cmd/sautictl/checkpoint_integration_test.go::TestCheckpointCommandRefusesARewrittenChain` | M2–M5 producers |
| 9 | Five separate stores | Verified | `internal/platform/pg/bootstrap/bootstrap_integration_test.go::TestBootstrapIsolatesStores` | P0 separate clusters |
| 10 | Consent flags gate the analytic export | Partial | `internal/platform/pg/schemas_integration_test.go::TestAnalyticRejectsUnconsentedRecord` | M6 pseudonymiser |
| 11 | Amendment gate | Verified | `tools/amendgate/check_test.go::TestCheckFailsGatedChangeWithoutAmendment`<br>`tools/amendgate/check_test.go::TestCheckFailsWhenHashDiffersFromAmendment`<br>`tools/amendgate/check_test.go::TestEffectiveGlobsCannotShrink`<br>`tools/amendgate/check_test.go::TestGatedSymlinkIsRejected` | M5 admin-screen enforcement |
| 12 | No new kind of record without the DPO | Partial | `internal/platform/pg/schemas_integration_test.go::TestMediaRecordingsRequireDestroyByAndKey`<br>`internal/config/config_test.go::TestLoadRejectsUnknownField` | Open gap: no gate yet requires DPO approval of a new record category (threat T-012). M5: DPO-owned record-category register, enforced in CI |
| 13 | No access from outside Uganda | Planned | `internal/platform/devkeys/devkeys_test.go::TestDevkeysRefuseProduction` | P0 |
| 14 | Dispatch first, fail safe to a human | Partial | `internal/audit/relay_test.go::TestRelayCatchesUpAfterLedgerOutage`<br>`internal/platform/africastalking/ussd_test.go::TestATInternalErrorGivesVoiceLine`<br>`internal/intake/service_test.go::TestTriageRuleErrorIsRedAndEscalated`<br>`internal/intake/sweeper_test.go::TestSweeperEscalatesDangerYesBeforeDispatch` | M2 escalation workflow (voice call) |

## Scenarios (Build Prompt §16)

The M0 demo is `tests/e2e/m0_fixture_test.go::TestM0FixtureIncident`. Every scenario below is planned for the milestone shown, and each will get an end-to-end test that checks T1–T7, events, audit entries and metrics.

| # | Scenario | Milestone |
|---|---|---|
| 1 | Red trauma: parallel alerts, first accept wins, arrival, payment | M2–M4 |
| 2 | Yellow obstetric cascade depth 3 | M2 |
| 3 | Poisoning with screen 5 = no is yellow | M1 — **verified:** `tests/e2e/m1_intake_test.go::TestScenario3PoisoningIsYellow` |
| 4 | Three bystanders, one crash: merge; coordinator split | M1 |
| 5 | USSD drops at screen 6: resume within 10 min or finish on IVR | M1 — **USSD part verified:** `tests/e2e/m1_intake_test.go::TestScenario5RedialResumesAtScreen6`, `tests/e2e/m1_intake_test.go::TestScenario5ResumedSessionCompletes`; IVR finish: M1 Part C |
| 6 | Somewhere else: 999/112 message; record kept and flagged | M1 — **verified:** `tests/e2e/m1_intake_test.go::TestScenario6SomewhereElse` |
| 7 | Nobody accepts: rings to 2.5 km, health centre acknowledges | M2 |
| 8 | First health centre silent: next nearest called | M2 |
| 9 | USSD push unanswered: voice at 20 s | M2 |
| 10 | Decline reason 3 stored and shown | M2 |
| 11 | Triage rule error: red and coordinator alerted | M1 red + flag + escalation event (`internal/intake/service_test.go::TestTriageRuleErrorIsRedAndEscalated`); M2 voice alert |
| 12 | Implausible arrival flagged | M3 |
| 13 | Payment timeout: status by reference, one disbursement | M4 |
| 14 | Primary DB fails over mid-cascade: no lost or duplicate alert | M2 |
| 15 | Inter-site link drops: Site B never self-promotes | M7 |
| 16 | Withdrawal then analytic restore: suppression replayed | M6 |
| 17 | Follow-up: 3 failed attempts, 50 h contact late | M5 |

## Review focus (M0 plan)

| Input class | Proof |
|---|---|
| Duplicate first callback, concurrent | `internal/platform/pg/intake_integration_test.go::TestConcurrentFirstCallbacksCreateOneIncident` |
| Phone number inside an error message | `internal/platform/logging/redact_test.go::TestRedactionMasksPhoneInsideErrorValues` |
| Phone-like string in an event payload | `internal/platform/pg/relay_integration_test.go::TestAuditEntryNeverCarriesPayloadContent` |
| Audit store down for minutes | `internal/audit/relay_test.go::TestRelayCatchesUpAfterLedgerOutage` |
| Missing monthly partition | `internal/platform/pg/opstore/opstore_integration_test.go::TestEventLandsInDefaultPartitionAndIsReported` |
