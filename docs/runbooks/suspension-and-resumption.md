# Runbook: suspending and resuming dispatch

| Field | Value |
|---|---|
| Owner | coordinator lead, with the Medical Director and PI |
| Severity | SEV-1 when the stopping rule fires; SEV-2 for a planned suspension |
| Alert / trigger | log line `DISPATCH SUSPENDED by the stopping rule`; a `stopping_rule_breached` row in the register; `sautictl dispatch status` shows `SUSPENDED` |
| Last reviewed: | 2026-11-13 |
| Last drilled | not yet drilled |

## Summary
While dispatch is suspended, intake continues and records are kept, but no responder is alerted and every incident is escalated to a coordinator (O6, [ADR-0026](../adr/ADR-0026-dispatch-suspension-and-stopping-rule.md)). Dispatch is suspended either by the stopping rule (red dispatch failure above 15% across 30 consecutive red incidents, Guide §14) or by a coordinator's decision. Only a person resumes it, and that decision is logged.

## Triggers and detection
- Automatic suspension: dispatch-worker logs `DISPATCH SUSPENDED by the stopping rule`, and the register gains a `stopping_rule_breached` row with its numbers, followed by `suspended stopping_rule`.
- Manual suspension: a coordinator ran `sautictl dispatch suspend` (later, the console).
- Check with `sautictl dispatch status --dsn <op_readonly DSN>`. It shows the state and the last ten register rows, each dated, with the staff identity or "automatic".

## Impact
**The dispatch path is deliberately stopped.** Callers hear the suspended message with their incident number (pending the Medical Director's and PI's approval of its wording) instead of "help is coming". Every incident is escalated (`dispatch_suspended`). Coordinators must handle each one: call the caller back, call a responder or a health centre. Alerts sent before the suspension may still be accepted.

## Diagnosis
1. `sautictl dispatch status --dsn <op_readonly DSN>`: who suspended dispatch, when, and why.
2. For a stopping-rule breach, list the red incidents behind it as `op_readonly`. The register row carries the failure count. For each recent red incident, check its T2 and T4 and whether it was escalated for lack of acceptance (`no_eligible_responder`, `red_no_acceptance`, `forwarded_to_facility` or `no_facility_acknowledged`).
3. Look for a common cause: responders unavailable in an area or at certain hours, an SMS outage (Part B), wrong gazetteer zones, or a dispatch-worker fault ([dispatch-worker-down.md](dispatch-worker-down.md)).

## Mitigation
1. **Tell the coordinators by voice** that dispatch is suspended and that every new incident is theirs. Brief the Medical Director and PI the same hour.
2. Record the event in the study's reportable-events process. The register row is the system's record of it; the study's report is separate.
3. Fix the cause. Do not resume while it persists, or the rule will fire again.
4. To suspend by hand (development and test only until the message is approved):

   ```sh
   sautictl dispatch suspend --dsn <op_console DSN> --reason coordinator_decision --staff <your staff subject UUID>
   ```

   In production this is refused while `messages.dispatch_suspended` carries `PENDING-MD-PI` or `PENDING-TRANSLATION`.
5. To resume, after review with the Medical Director and PI:

   ```sh
   sautictl dispatch resume --dsn <op_console DSN> --reason <code, e.g. reviewed_with_md_pi> --staff <your staff subject UUID>
   ```

   A resumption always carries the staff identity. Incidents still open are re-planned at once.
6. Never update `op.dispatch_control` or the register by hand: the register is append-only and the database refuses edits.

## Escalation
Medical Director and PI by voice within 1 hour of an automatic suspension. The Data Protection Officer is informed if the cause involves personal data, such as a messaging fault.

## Verification
- `sautictl dispatch status` shows `active` and the `resumed` row with your staff identity.
- New incidents are alerted again; check the first alert after resumption on the console.
- The audit ledger holds `dispatch.suspended` and `dispatch.resumed` entries (the outbox relay delivers them).

## References
[ADR-0026](../adr/ADR-0026-dispatch-suspension-and-stopping-rule.md), [ADR-0024](../adr/ADR-0024-dispatch-planners-and-postgresql-scheduler.md), [cli-sautictl.md](../reference/cli-sautictl.md), Guide §14, spec O6.
