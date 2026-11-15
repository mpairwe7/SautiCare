---
status: accepted
date: 2026-11-15
decision-makers: Sauti engineering (repository owner)
consulted: the November 2026 evaluation audit (V-04, V-29, V-33); Build Prompt §3 rule 3, §14; ADR-0003; ADR-0019; ADR-0041
informed: PI, Medical Director, emergency medicine co-investigators, Data Protection Officer, technical operator
---
# ADR-0043: Only the approved bundle runs, and every classification names it

## Context and Problem Statement

[ADR-0019](ADR-0019-signed-triage-decision-table.md) lets a triage bundle run when its signature verifies against a key trusted in `config/gated/triage.yaml`, and stamps the bundle's logic version on `incident.classified`. The [evaluation audit](../explanation/evaluation-audit-2026-11.md) found two gaps in that design (V-29), and one in how its safety properties are proven (V-04):
- **Which signed bundle runs is not pinned.** `config/gated/triage.yaml` names a file and keys, no digest. Any bundle the trusted key ever signed loads, including an older one or one withdrawn after an amendment.
- **A classification can be stored without a logic version.** The store sends an empty version as NULL, and the format check in the append function does not raise on NULL. The audit's probe stored a yellow classification that no rule set can be found for.
- **The safety invariants are proven by single examples.** Three mutants that let a severity drop one step passed every test (two in Go, one in the SQL guard). The fold accepted a classification with no valid severity that the SQL refused. An out-of-domain answer to one of the four inputs could be classified green with no rule error. Nothing kept a future package from importing `internal/triage`.

How does Sauti pin the bundle that runs, make every classification attributable, and prove that no event lowers a severity?

## Decision Drivers

* Hard constraint 3: no rule and no model lowers a severity. Build Prompt §14: a rule error is red and alerts a coordinator.
* The study must attribute every classification to the exact rule set that made it (AIR-002).
* After an amendment, the bundle it replaced must not run again, even though the same key signed it.
* No new record kind, table or column (hard constraint 12), and the event-append function is not replaced: M3's migration 0029 replaces it once, and a later replacement would undo this change.
* ADR-0041 rule 2: safety invariants are property tests, in Go and, where the database enforces the rule, in SQL.

## Considered Options

* **A digest of the approved bundle in the gated configuration, a trigger on the event log, and property tests.**
* Pin the bundle by its version string: rejected. The version is written inside the bundle, so a re-signed edit can keep it.
* Revoke the key after each amendment: rejected. It forces a new clinical key per amendment and still lets any bundle the new key signs run.
* Check the logic version inside `op.append_event_core`: rejected. It replaces the append function, which M3's 0029 replaces again.

## Decision Outcome

Chosen option: **a digest, a trigger and property tests.** This supersedes ADR-0019 in part: the signature still proves who signed a bundle, and the digest now decides which signed bundle runs.

1. **Only the approved bundle runs.** `config/gated/triage.yaml` carries `approved_sha256`, the lower-case hex sha256 of `triage-rules/tool1.yaml`. `config.Load` refuses a value that is not 64 lower-case hex digits. When it is set, `wiring.BuildIntake` refuses a bundle whose bytes hash differently, in every environment, after the signature check. With `SAUTI_ENV=production` it also refuses an empty `approved_sha256`. The file is gated, so a new digest is a protocol change under the amendment gate, and it changes the configuration fingerprint.
2. **A classification needs a logic version.** Migration 0039 adds `op.refuse_unversioned_classification()` and a `BEFORE INSERT` trigger on `op.incident_events`. The trigger refuses an `incident.classified` event whose logic version is NULL or empty, with SQLSTATE `SA422`. It replaces no function, so it holds whatever a later migration makes of the write path. `incident.Apply` refuses the same event with `ErrIllegalTransition`.
3. **The ratchet is a property, in Go and in SQL.** For every pair of severities, a classification may keep or raise the severity and `severity_raised` must raise it. Both the fold and the SQL write path are tested over all nine pairs of each event, on real histories. A fuzz property, also run over 20 000 generated histories in every unit run, checks that no accepted event lowers the severity or stores something that is not a severity. The fold refuses every payload the SQL refuses, and the reverse.
4. **An out-of-domain input is red with a rule error.** This is checked for each of the four inputs at every point of the declared domain, and by the fuzz oracle.
5. **Only intake, the wiring and `sautictl` import triage.** A new archcheck rule, R5, refuses any other importer, so a future speech-assist package cannot classify (V-33). Test files are exempt.

### Consequences

* Good: the audit's surviving mutants now fail tests: the one-step drops at `fold.go:65` and in the SQL guard of migration 0018, `severity_raised` allowed to lower at `fold.go:155` (and its SQL twin in 0017), the fold accepting an invalid severity (I1) or an empty flag, and the domain checked for two inputs of four.
* Good: an older or withdrawn bundle signed by a trusted key no longer starts. Every stored classification names its rule set.
* Bad: every re-signed bundle needs a second gated edit, the new digest, or intake refuses to start. EV-4 re-signs the bundle and updates the digest in the same change.
* Bad: a writer that classifies without a logic version now fails, in Go and in SQL. No production path does; the unit fixtures of `internal/incident` now stamp a version.
* Neutral: the configuration fingerprint changes, because `config/gated/triage.yaml` changed. With the study window inactive, the amendment gate passes it.

### Confirmation

* `internal/platform/wiring/intake_test.go::TestBuildIntakeRefusesAnUnapprovedBundle`, `::TestProductionRequiresAnApprovedDigest`; `internal/config/config_test.go::TestLoadRefusesAMalformedApprovedDigest`.
* `internal/incident/fold_test.go::TestApplyRequiresLogicVersionOnClassification`; `internal/platform/pg/opstore/opstore_integration_test.go::TestAppendRequiresLogicVersionOnClassification`.
* `internal/incident/fold_test.go::TestNoEventLowersSeverity`, `::TestApplyRejectsMalformedPayloads`; `internal/incident/fuzz_test.go::TestFoldSeverityNeverFallsProperty` and the fuzz target `FuzzFoldSeverityNeverFalls` in the same file (in `scripts/ci/fuzz-smoke.sh`); `internal/platform/pg/opstore/opstore_integration_test.go::TestAppendRejectsEverySeverityLowering`; `internal/platform/pg/opstore/tool1_integration_test.go::TestFoldRejectsWhatSQLRejects`, `::TestSQLRejectsWhatFoldRejects`.
* `internal/triage/triage_test.go::TestRuntimeErrorIsRed`, `::TestLoadRejectsNonTotalTable`, and the oracle of the fuzz target `FuzzClassifyNeverPanics` in `internal/triage/fuzz_test.go`.
* `tools/archcheck/check_test.go::TestOnlyIntakeWiringAndSautictlImportTriage`; `go run ./tools/archcheck` in CI.
* Each mutant above was applied by hand, seen to fail at least one of these tests, and reverted (the PR description lists the runs).
