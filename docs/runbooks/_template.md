# Runbook: <title>

| Field | Value |
|---|---|
| Owner | <role, never a personal name> |
| Severity | <SEV-1 … SEV-4> |
| Alert / trigger | <metric, log signal or schedule> |
| Last reviewed: | <YYYY-MM-DD> |
| Last drilled | <YYYY-MM-DD or "not yet drilled"> |

## Summary
What this runbook is for, in two sentences.

## Triggers and detection
The alert, its threshold and where it fires.

## Impact
What callers, responders, coordinators and the study lose while this lasts. State explicitly whether the dispatch path is affected.

## Diagnosis
Numbered, copy-pasteable steps. Commands use least-privileged roles.

## Mitigation
Numbered steps. Never edit event, audit or consent rows by hand. Any deviation from protocol is logged as a deviation record.

## Escalation
Who, by which channel (voice for clinical matters — ISP §5.6), and within what time.

## Verification
How to prove the system is healthy again.

## References
ADRs, reference docs, policy sections (by number).
