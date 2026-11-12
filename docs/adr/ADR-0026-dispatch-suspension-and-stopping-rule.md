---
status: accepted
date: 2026-11-13
decision-makers: Sauti engineering (repository owner); O6 decided by the user on 2026-11-12
consulted: Engineering Guide §14 (stopping rules); Build Prompt §3 rule 14, §19; spec O6
informed: Medical Director, PI, coordinators, Data Protection Officer
---
# ADR-0026: Dispatch suspension and the stopping rule

## Context and Problem Statement

The study stops automatic dispatch when it is failing. Its stopping rule is red dispatch failure above 15% across 30 consecutive red incidents (Guide §14), and a coordinator may also suspend dispatch. O6 asked what happens while dispatch is suspended. The user decided on 2026-11-12: "Escalate + placeholder msg". Intake continues and records are kept. No responder is alerted. Every incident is escalated to the coordinator. The caller's closing message is a PENDING placeholder, and production refuses to enable suspension until the Medical Director and PI approve its wording. How are suspension, the rule and the register built?

## Decision Drivers

* Fail safe to a human (rule 3.14): suspension must never drop an incident.
* Suspending and resuming are reportable. Each needs a dated record, staff identity and an audit entry; resuming needs a logged staff decision.
* A suspension must not feed the rule that caused it.

## Considered Options

* **One control row and an append-only control log in the operational store**; the planner reads it, and intake reads it at the closing screen.
* A feature flag in configuration (requires a deploy; no staff identity).
* A per-incident "suspended" flag.

## Decision Outcome

Chosen option: **control row and register** (migration 0016).
- `op.dispatch_control` is a single row: suspended or active, reason, staff and time.
  - `op.suspend_dispatch` and `op.resume_dispatch` are for the console role and require a staff identity.
  - `op.auto_suspend_dispatch` is for the dispatch leader. It only suspends; first it registers the breach with its numbers.
  - Every change adds a row to the append-only `op.dispatch_control_log` (the reportable-events register).
  - Every change becomes an audit-ledger entry with no incident (`dispatch.suspended`, `dispatch.resumed`, `dispatch.stopping_rule_breached`).
  - Every change wakes every incident in dispatch.
- While dispatch is suspended:
  - The planner sends no new alert and forwards nothing.
  - Alerts already in flight may still be accepted; stand-downs and lapses are still recorded.
  - Each incident is escalated once (`dispatch_suspended`).
  - Intake records everything as usual. Its closing screen, on USSD and voice, is `messages.dispatch_suspended` with the incident number, never "help is coming", and it escalates with the same reason.
- The suspended message is marked `PENDING-MD-PI`. Production refuses that marker. `sautictl dispatch suspend` refuses to suspend in production until the message is approved (`config.Bundle.SuspensionReady`).
- The stopping rule is gated configuration: `stopping_rule: {window: 30, max_failure_rate: 0.15}`.
  - A red incident succeeds when any responder (either role) accepts within the red window of T2 (`red_escalation_after`, 3 minutes: decision 2's protocol answer), and fails when the window passes without one. *Revised after the PR #10 review (I2); the definition is for the PI to confirm.*
  - Not judged: an incident closed inside its window without an acceptance (a caller's or coordinator's decision, not a dispatch outcome), one whose window is not over, and merged incidents.
  - Only incidents dispatched after the latest resumption are judged. A coordinator's resume is therefore never undone by the evidence that caused the suspension, and a suspension cannot feed the rule.
  - The rule is checked after every pass. Exactly 15% does not breach, and fewer than 30 settled red incidents are not judged.

### Consequences

* Good: a suspension takes effect within one poll interval and is reversible only by a person. The register and the ledger show who did what and when.
* Bad: until the Medical Director and PI approve the wording, suspension can be used in development and testing only.
* Neutral: the console screens (M5) call the same functions as `sautictl dispatch`.

### Confirmation

`internal/dispatch/stopping_test.go::TestStoppingRuleBoundaries`; `internal/platform/pg/opstore/control_integration_test.go::TestRedDispatchOutcomes` (anchoring, time-based success, closure inside the window); `internal/intake/suspension_test.go::TestMergedCallerWhileSuspendedHearsTheMessage`; `internal/dispatch/engine_test.go::TestCheckStoppingRule`; `internal/platform/pg/opstore/control_integration_test.go::TestSuspensionControl`, `::TestAutoSuspendRegistersTheBreach`, `::TestRedDispatchOutcomes`; `internal/intake/suspension_test.go::TestSuspendedDispatchChangesTheClosingMessageAndEscalates`; `internal/config/tool1_test.go::TestSuspensionReady`, `::TestProductionReadyRefusesPlaceholderTool1`; `cmd/sautictl/main_test.go::TestDispatchCommandsGateAndValidate`; `cmd/sautictl/dispatch_integration_test.go::TestDispatchSuspendStatusResume`; `internal/dispatch/plan_test.go::TestSuspendedNeverAlertsAndEscalates`, `::TestSuspensionMidCascade`; `internal/platform/wiring/dispatch_integration_test.go::TestEngineSuspendedMidCascade`.
