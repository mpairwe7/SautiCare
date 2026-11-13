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
| R10 | No patient information on consumer messaging; voice escalation logged | ISP §5.6 | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesUnlistedHost`<br>`internal/config/sms_test.go::TestSMSTemplatesAreCompliant`<br>`internal/platform/channels/sms_test.go::TestAlertSMS` | M2 Part B: voice alert content; M5: logged voice escalation |
| R11 | Retention schedule, holds, destruction log | RMRP §2–4 | Partial | `internal/platform/pg/schemas_integration_test.go::TestMediaRecordingsRequireDestroyByAndKey` | M6: retention-agent, crypto-shredding |
| R12 | Breach timers 24 h / 48 h / 72 h / 7 days and register | ISP §7 | Planned | — | M5: breach workflow |
| R13 | Data-subject requests in 14 days; withdrawal removes analytic record | ISP §8 | Partial | `internal/platform/pg/schemas_integration_test.go::TestSuppressionLedgerAppendOnly`<br>`internal/platform/pg/schemas_integration_test.go::TestSuppressionDeletesAndBlocksMEDSRecord` | M5/M6: DSR workflow, restore replay |
| R14 | Triage logic versioned; logic version on every record; overrides logged | DPIA risk 8 | Partial | `internal/config/config_test.go::TestFingerprintStableAndSensitive`<br>`internal/incident/fold_test.go::TestApplyRejectsSeverityLowering`<br>`internal/triage/triage_test.go::TestLoadRejectsBadSignature`<br>`internal/triage/triage_test.go::TestLogicVersionAndDevSigned`<br>`tests/e2e/m1_intake_test.go::TestScenario3PoisoningIsYellow` | M5: overrides logged |
| R15 | Pseudonymised outputs with small-cell checks; minimal payment payloads | ISP §5.11 | Partial | `internal/platform/pg/schemas_integration_test.go::TestAnalyticRejectsUnconsentedRecord` | M4: payment payloads; M6: disclosure control |

## Hard constraints (Build Prompt §3)

| # | Constraint | Status | Proof (M0) | Remaining |
|---|---|---|---|---|
| 1 | Data stays in Uganda | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesRedirectToUnlistedHost` | P0: hosting, approved-host locations, network controls, backups |
| 2 | Basic phone first | Partial | `internal/platform/africastalking/ussd_test.go::TestATParsesFormAndRendersCONEND`<br>`internal/intake/machine_test.go::TestEveryRenderedScreenFitsLimitAndGSM7`<br>`tests/e2e/m1_intake_test.go::TestScenario5ResumedSessionCompletes`<br>`tests/e2e/m1_ivr_test.go::TestScenario5FinishOnIVR` | M2 responder channels |
| 3 | Deterministic triage and dispatch; no model lowers severity | Partial | `internal/incident/fold_test.go::TestApplyRejectsSeverityLowering`<br>`internal/platform/pg/opstore/opstore_integration_test.go::TestAppendRejectsSeverityLowering`<br>`internal/triage/triage_test.go::TestRuleTableFromBuildPrompt`<br>`internal/triage/triage_test.go::TestLoadRejectsNonTotalTable`<br>`internal/incident/merge_test.go::TestSeverityRaised`<br>`internal/platform/pg/opstore/dedup_integration_test.go::TestMergeCarriesSeverityAndEscalation`<br>`internal/incident/tool1_test.go::TestApplyRefusesAChangedAnswer`<br>`internal/platform/pg/opstore/tool1_integration_test.go::TestSQLRefusesAChangedAnswer`<br>`internal/dispatch/rank_test.go::TestRankIsTotalAndStable`<br>`internal/dispatch/plan_test.go::TestPlanIsIdempotent`<br>`internal/platform/wiring/dispatch_test.go::TestDispatchLogicVersion` | M2 Part B/C channels |
| 4 | Server-side time | Verified | `internal/platform/pg/opstore/opstore_integration_test.go::TestOpenIncidentSetsT1FromDatabase`<br>`internal/platform/pg/schemas_integration_test.go::TestIntakeCannotSupplyUSSDStepTimestamp` | M3 attested T5 verification |
| 5 | Never reveal the caller's number | Partial | `internal/platform/ivr/bridge_test.go::TestBridgeResponderToCaller` (masked bridge: Sauti's number shown, no number logged)<br>`internal/platform/wiring/scenarios_integration_test.go::TestScenario1RedTraumaThroughTheChannels`<br>`internal/pii/pii_test.go::TestPhoneMasksEverywhere`<br>`internal/platform/logging/redact_test.go::TestRedactionMasksPhoneInsideErrorValues`<br>`internal/platform/africastalking/ussd_test.go::TestATLogsAKeyedCorrelationValueNeverTheSessionID`<br>`internal/intake/service_test.go::TestCallbackNumberNeverInPayloadOrSteps`<br>`internal/platform/pg/opstore/tool1_integration_test.go::TestSQLRejectsWhatFoldRejects`<br>`tests/e2e/m1_intake_test.go::TestNoCallerNumberInLogs` | M2 masked bridge; M5 audited reveal |
| 6 | No patient data on messaging apps | Partial | `internal/platform/egress/egress_test.go::TestEgressDeniesUnlistedHost`<br>`internal/config/sms_test.go::TestSMSTemplatesAreCompliant`<br>`internal/platform/channels/sms_test.go::TestAlertSMS`<br>`internal/platform/channels/sms_test.go::TestCallerNotices` | M2 Part B voice alert content |
| 7 | No coordinator override outside exceptions | Planned | — | M5 |
| 8 | Audit trail, incident and staff IDs only | Verified | `internal/platform/pg/relay_integration_test.go::TestAuditEntryNeverCarriesPayloadContent`<br>`internal/platform/pg/auditstore/auditstore_integration_test.go::TestSQLHashMatchesGoCanonical`<br>`internal/platform/pg/opstore/opstore_integration_test.go::TestAuditPayloadHashIsSalted`<br>`internal/platform/pg/tamper_integration_test.go::TestOwnerRewriteWithRecomputedChainIsCaughtByWitness`<br>`internal/audit/witness_test.go::TestAnchoredCheckpointerUsesTheLatestWitnessEachTerm`<br>`internal/audit/witness_test.go::TestRelayCheckpointRefusesARewriteBehindAManualCheckpoint`<br>`internal/audit/witness_test.go::TestCheckpointerChecksEveryNewWitnessNote`<br>`cmd/sautictl/checkpoint_integration_test.go::TestCheckpointCommandRefusesARewrittenChain`<br>`cmd/sautictl/checkpoint_integration_test.go::TestRelayCheckpointRefusesARewriteBehindTheCommandsCheckpoint`<br>`internal/platform/pg/opstore/control_integration_test.go::TestSuspensionControl` | M3–M5 producers |
| 9 | Five separate stores | Verified | `internal/platform/pg/bootstrap/bootstrap_integration_test.go::TestBootstrapIsolatesStores` | P0 separate clusters |
| 10 | Consent flags gate the analytic export | Partial | `internal/platform/pg/schemas_integration_test.go::TestAnalyticRejectsUnconsentedRecord` | M6 pseudonymiser |
| 11 | Amendment gate | Verified | `tools/amendgate/check_test.go::TestCheckFailsGatedChangeWithoutAmendment`<br>`tools/amendgate/check_test.go::TestCheckFailsWhenHashDiffersFromAmendment`<br>`tools/amendgate/check_test.go::TestEffectiveGlobsCannotShrink`<br>`tools/amendgate/check_test.go::TestGatedSymlinkIsRejected` | M5 admin-screen enforcement |
| 12 | No new kind of record without the DPO | Partial | `internal/platform/pg/schemas_integration_test.go::TestMediaRecordingsRequireDestroyByAndKey`<br>`internal/config/config_test.go::TestLoadRejectsUnknownField` | Open gap: no gate yet requires DPO approval of a new record category (threat T-012). M5: DPO-owned record-category register, enforced in CI |
| 13 | No access from outside Uganda | Planned | `internal/platform/devkeys/devkeys_test.go::TestDevkeysRefuseProduction` | P0 |
| 14 | Dispatch first, fail safe to a human | Partial | `internal/audit/relay_test.go::TestRelayCatchesUpAfterLedgerOutage`<br>`internal/platform/africastalking/ussd_test.go::TestATInternalErrorGivesVoiceLine`<br>`internal/intake/service_test.go::TestTriageRuleErrorIsRedAndEscalated`<br>`internal/intake/sweeper_test.go::TestSweeperEscalatesDangerYesBeforeDispatch`<br>`internal/intake/sweeper_test.go::TestSweeperEscalatesRedBeforeDispatch`<br>`internal/intake/sweeper_test.go::TestSweeperCommitsAbandonmentAndEscalationTogether`<br>`internal/platform/pg/opstore/idle_integration_test.go::TestListIdleRelistsAnAbandonmentThatOwesItsEscalation`<br>`internal/intake/atomicity_test.go::TestEveryWriteFailureConverges`<br>`internal/intake/atomicity_test.go::TestRuleErrorFlagAndEscalationSurviveAFailure`<br>`internal/platform/ivr/dialplan_test.go::TestDialplanFallsBackToACoordinator`<br>`internal/platform/ivr/controller_test.go::TestControllerIntakeErrorHandsOverToACoordinator`<br>`internal/dispatch/plan_test.go::TestNoEligibleEscalatesImmediately`<br>`internal/platform/wiring/dispatch_integration_test.go::TestEngineNoEligibleEscalatesAtOnce`<br>`internal/platform/wiring/dispatch_integration_test.go::TestEngineRunOnceEscalatesAnUnprocessableIncident`<br>`internal/platform/wiring/dispatch_integration_test.go::TestEngineSuspendedMidCascade`<br>`internal/intake/suspension_test.go::TestSuspendedDispatchChangesTheClosingMessageAndEscalates`<br>`internal/intake/watchdog_test.go::TestWatchdogEscalatesStalledDispatch`<br>`internal/dispatch/engine_test.go::TestPassDeadlineAndLastPass`<br>`cmd/dispatch-worker/main_test.go::TestReadinessFailsWhenTheLeaderStalls`<br>`cmd/dispatch-worker/main_test.go::TestAwaitStopsTheLeaderBeforeThePoolCloses`<br>`internal/platform/pg/opstore/review10cr_integration_test.go::TestClaimReturnsTheMostUrgentFirst`<br>`internal/dispatch/deliver_test.go::TestAPlannedIncidentSendsWhileLaterOnesPlan`<br>`internal/platform/ivr/outbound_test.go::TestANewTermFreesLinesWhoseCallsEnded`<br>`internal/platform/ivr/outbound_test.go::TestAnAnsweredCallWithNoInputIsHungUp`<br>`internal/platform/ivr/bridge_test.go::TestBridgeFailuresGoToACoordinator` (masked bridge)<br>`internal/platform/ivr/bridge_test.go::TestBridgeChoiceWithNoKeyGoesToACoordinator`<br>`internal/platform/ivr/bridge_test.go::TestBridgePromptThatCannotPlayGoesToACoordinator` | M2 Part B coordinator voice call |

## Scenarios (Build Prompt §16)

The M0 demo is `tests/e2e/m0_fixture_test.go::TestM0FixtureIncident`. Every scenario below is planned for the milestone shown, and each will get an end-to-end test that checks T1–T7, events, audit entries and metrics.

| # | Scenario | Milestone |
|---|---|---|
| 1 | Red trauma: parallel alerts, first accept wins, arrival, payment | M2 — **verified to acceptance** through the channels: `internal/platform/wiring/scenarios_integration_test.go::TestScenario1RedTraumaThroughTheChannels` (parallel SMS alerts, first menu accept wins, "already taken", stand-downs by SMS, caller told, then the masked bridge); arrival M3, payment M4 |
| 2 | Yellow obstetric cascade depth 3 | M2 — **verified**: `internal/platform/wiring/scenarios_integration_test.go::TestScenario2YellowObstetricThroughTheChannels` (through the menu), `internal/platform/wiring/dispatch_integration_test.go::TestEngineYellowCascadeDepthThree`, `internal/dispatch/plan_test.go::TestScenario2YellowCascadeDepthThree` |
| 3 | Poisoning with screen 5 = no is yellow | M1 — **verified:** `tests/e2e/m1_intake_test.go::TestScenario3PoisoningIsYellow` |
| 4 | Three bystanders, one crash: merge; coordinator split | M1 — **verified:** `tests/e2e/m1_dedup_test.go::TestScenario4ThreeBystandersOneIncident` |
| 5 | USSD drops at screen 6: resume within 10 min or finish on IVR | M1 — **verified:** `tests/e2e/m1_intake_test.go::TestScenario5RedialResumesAtScreen6`, `tests/e2e/m1_intake_test.go::TestScenario5ResumedSessionCompletes`, `tests/e2e/m1_ivr_test.go::TestScenario5FinishOnIVR` |
| 6 | Somewhere else: 999/112 message; record kept and flagged | M1 — **verified:** `tests/e2e/m1_intake_test.go::TestScenario6SomewhereElse` |
| 7 | Nobody accepts: rings to 2.5 km, health centre acknowledges | M2 — **verified** (amended mode): `internal/platform/wiring/forward_integration_test.go::TestScenario7ForwardAcknowledged`, `internal/dispatch/plan_test.go::TestScenario7NobodyAcceptsForwardsToNearest` |
| 8 | First health centre silent: next nearest called | M2 — **verified** (amended mode): `internal/platform/wiring/forward_integration_test.go::TestScenario8NextCentre`, `internal/dispatch/plan_test.go::TestScenario8NoAcknowledgementTriesNextNearest` |
| 9 | USSD push unanswered: voice at 20 s | M2 — **verified** (Part B; USSD push disabled, O2, so the SMS goes at T0): `internal/platform/wiring/voice_integration_test.go::TestScenario9VoiceAt20Seconds`, `internal/platform/ivr/alerts_test.go::TestAlertCallsDialDueAlertsOnce` |
| 10 | Decline reason 3 stored and shown | M2 — **verified** (Part B, amended mode): `internal/platform/wiring/respond_integration_test.go::TestScenario10DeclineThroughTheMenu`, `internal/platform/pg/opstore/respond_integration_test.go::TestScenario10DeclineReasonStoredAndShown` (history read as `op_readonly` and `op_console`), `internal/respond/ussd_test.go::TestARetriedDeclineGetsTheSameAnswer` |
| 11 | Triage rule error: red and coordinator alerted | M2 — **verified**: red + flag + escalation (`internal/intake/service_test.go::TestTriageRuleErrorIsRedAndEscalated`), coordinator voice call until acknowledged (`internal/platform/wiring/forward_integration_test.go::TestScenario11RuleErrorRingsTheCoordinator`) |
| 12 | Implausible arrival flagged | M3 |
| 13 | Payment timeout: status by reference, one disbursement | M4 |
| 14 | Primary DB fails over mid-cascade: no lost or duplicate alert | M2 — **verified** (engine and store): `internal/platform/wiring/dispatch_integration_test.go::TestEngineFailoverMidCascade` (recovery through the lease), `::TestEngineCrashBetweenRecordAndDelivery`, `::TestEngineCrashAfterTheAttemptClaim`, `internal/platform/pg/opstore/dispatch_integration_test.go::TestAttemptsAreClaimedOnce` |
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

## Review focus (M2 plan, Part A)

| Input class | Proof |
|---|---|
| Crash or failover between "decide to alert" and "alert sent" | `internal/platform/wiring/dispatch_integration_test.go::TestEngineCrashBetweenRecordAndDelivery`, `::TestEngineCrashAfterTheAttemptClaim`, `::TestEngineFailoverMidCascade`; `internal/platform/pg/opstore/dispatch_integration_test.go::TestAttemptsAreClaimedOnce` |
| Two responders accept one role at the same instant | `internal/platform/pg/opstore/dispatch_integration_test.go::TestConcurrentAcceptsOneAssignment` |
| One responder accepts alerts for two incidents at the same instant | `internal/platform/pg/opstore/review10cr_integration_test.go::TestAcceptsSerialisePerResponder` |
| No eligible responder at all | `internal/dispatch/plan_test.go::TestNoEligibleEscalatesImmediately`, `internal/dispatch/plan_test.go::TestAmendedEmptyRingOneEscalatesAtOnce`, `internal/dispatch/plan_test.go::TestEscalationsAreOncePerReasonAndRole`; `internal/platform/wiring/dispatch_integration_test.go::TestEngineNoEligibleEscalatesAtOnce` |
| Availability or a gate lapsing mid-cascade | `internal/platform/pg/opstore/dispatch_integration_test.go::TestIssueAlertGuards`; `internal/platform/wiring/dispatch_integration_test.go::TestEngineMovesOnWhenTheDatabaseRefusesAResponder` (the 8-hour expiry reminder SMS arrives with Part B) |
| Suspension switched on mid-cascade | `internal/dispatch/plan_test.go::TestSuspensionMidCascade`, `::TestForwardsSettleWhenDispatchStopsSearching`; `internal/platform/wiring/dispatch_integration_test.go::TestEngineSuspendedMidCascade`, `internal/platform/wiring/dispatch_integration_test.go::TestEngineDoesNotDeliverAfterADatabaseSuspension`, `::TestEngineDoesNotForwardAfterADatabaseSuspension`; `internal/platform/pg/opstore/review10_integration_test.go::TestNoAlertOrAttemptWhileSuspended`; `internal/platform/pg/opstore/review10cr_integration_test.go::TestNoForwardOrAlertOnceASuspensionCommits` |

## Review focus (M2 Part B, CodeRabbit PR #11)

| Input class | Proof |
|---|---|
| A later incident's plan hangs to its pass timeout | `internal/dispatch/deliver_test.go::TestAPlannedIncidentSendsWhileLaterOnesPlan` |
| An incident closes with a forward still open, or while its call is being claimed | `internal/platform/pg/opstore/forward_integration_test.go::TestNoForwardAttemptOnceTheIncidentCloses` |
| A coordinator call answered before it is stored; its end lost across a leadership change; answered and never keyed | `internal/platform/ivr/outbound_test.go::TestAnEarlyAnswerKeepsTheLine`, `::TestANewTermFreesLinesWhoseCallsEnded`, `::TestAnAnsweredCallWithNoInputIsHungUp`; `internal/platform/ari/client_test.go::TestClientChannelExists` |
| A key the menu being heard does not offer | `internal/platform/ivr/alerts_test.go::TestAnInvalidKeyDoesNotCutTheAlertPrompt`, `internal/platform/ivr/outbound_test.go::TestAnInvalidKeyDoesNotCutAnOutboundPrompt` |
| A menu callback retried after its decline committed; an open assignment beside a pending alert | `internal/respond/ussd_test.go::TestARetriedDeclineGetsTheSameAnswer`; `internal/platform/pg/opstore/respond_integration_test.go::TestResponderMenuReads` |
| An off-duty availability screen over the limit | `internal/respond/ussd_test.go::TestAvailabilityScreensAreCheckedInBothStates` |
