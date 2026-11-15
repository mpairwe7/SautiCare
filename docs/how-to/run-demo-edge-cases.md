# How to run the demo edge-case suite

**Type:** How-to (Diátaxis) · **Code:** `cmd/demo/edge_integration_test.go` · **Decision:** [ADR-0039](../adr/ADR-0039-public-demo-on-hugging-face.md)

`TestDemoEdgeCases` drives the demo (`cmd/demo`) over its HTTP API only, as a presenter would. It covers the cases of the Scalable MVP Stack, §Edge cases, that the demo can show. Where Sauti's rule differs from that reference, the suite asserts Sauti's rule. Each scenario starts with a Reset and ends with a passing audit verification (ledger OK, checkpoint and witness OK, zero projection drift).

## Run it locally

You need PostgreSQL 18 programs (see [the prototype tutorial](../tutorials/run-the-prototype-locally.md), step 3). The test builds the demo, starts it on a free loopback port and stops it with SIGTERM at the end:

```bash
SAUTI_DEMO_PG_BIN=/path/to/postgresql-18/bin \
  go test -tags integration ./cmd/demo/ -run TestDemoEdgeCases -count=1 -v
```

Without `SAUTI_DEMO_PG_BIN`, the test looks in the embedded-PostgreSQL cache and on `$PATH`. If it finds no PostgreSQL 18, it skips. A full run takes about a minute.

## Run it against a deployed demo

Set `SAUTI_DEMO_URL` to the demo's address. The test then starts nothing and talks to that URL:

```bash
SAUTI_DEMO_URL=https://<owner>-<space>.hf.space \
  go test -tags integration ./cmd/demo/ -run TestDemoEdgeCases -count=1 -v
```

- The suite presses **Reset** before every scenario. Everything on the demo is erased, so run it only when nobody is presenting.
- Other visitors' calls would change what the board shows. Run it when the Space is otherwise idle.
- Every wait is bounded. The first wait (5 minutes) covers a sleeping Space waking up, and each Reset may take 2 minutes.
- The demo allows 60 dials a minute. The suite stays well under that, but another visitor's dials count too.
- A private Space needs an access token that the test does not send. Run it against a public Space.

To run one scenario, name it: `-run 'TestDemoEdgeCases/ConcurrentAcceptance'`.

## What each scenario asserts

| Scenario | Reference row | Sauti's rule, as asserted |
|---|---|---|
| `SessionDropAndResume` | 1 | The incident exists from the first screen. After a drop at the callback screen, dispatch has already started (T2 after screen 7, spec D5). A redial from the same phone resumes at that screen without asking, on the same incident (`session.resumed`). The window is `resume_window`, 10 minutes. The reference has 30 (ADR-0009). |
| `PossibleDuplicates` | 3 | Same category and place close together: both callers hear HELP IS COMING with their own numbers, and the ledger verifies. Sauti merges the later call automatically at T2 ([ADR-0022](../adr/ADR-0022-deduplication-merge-and-split.md)). The reference flags it for a coordinator. The board must show the later call merged into the first, which records the merge. |
| `ConcurrentAcceptance` | 11 | VHT 1 and VHT 2 press 1 at the same moment. Exactly one hears Accepted; the other hears "Already taken" (or "No longer needed" if the stand-down came first). Transporter 1 can still accept for its role. The board shows one acceptance per role ([ADR-0025](../adr/ADR-0025-dispatch-store-and-first-accept-wins.md)). |
| `NoResponderEscalates` | 12 | A Ward 3 call has nobody in ring 1. In `protocol` mode both roles escalate to a coordinator at once (`no_eligible_responder`). The reference tries further responders first and calls the coordinator by voice. The demo has no voice, so the board shows the escalation. |
| `DeclineCascades` | 12 | A green Ward 2 call alerts VHT 4 alone. VHT 4 declines, and a second-wave VHT alert follows. A red call alerts the top three of each role at once, so a decline there leaves the others alerted. |
| `Surge` | 14 | Five red calls in Ward 2: five numbers, and all five incidents on the board, red and dispatching. None is merged or dropped. Ordering by severity is not observable: the board lists the newest first, and the queue is internal (`op.dispatch_surge`, [ADR-0028](../adr/ADR-0028-masked-bridge-and-surge.md)). |
| `RepeatCaller` | 9 | One phone reports three times. Every call is served and dispatched, and none is flagged or blocked. Sauti has no prank flag. The reference has one, set only by a coordinator. |
| `Luganda` | 5 | The Luganda path, end to end. Every screen fits `default_screen_char_limit` (or the network's `screen_char_limits` entry) in `config/gated/intake.yaml` and stays in GSM-7. |
| `NoFullNumberAnywhere` | — | No response of the whole run, USSD screens included, holds a demo phone's full number (`+2567` and 8 digits, or the 9 national digits). |
| `ResetEmptiesEverything` | — | After a call, Reset leaves an empty board and empty inboxes. |

## Findings

**F1, fixed: the board failed after a merge.** When the planner has no next wake-up, as for a merged incident, `op.schedule_dispatch` stores `due_at = 'infinity'`. The board scanned that column into a Go `time.Time`, which cannot hold infinity. `GET /api/board` therefore answered 500 until Reset, though intake, dispatch and the ledger were unaffected. The board now reads only finite timers (`isfinite(due_at)`), so a merged incident shows no wake-up. `PossibleDuplicates` caught it, and it now passes.
