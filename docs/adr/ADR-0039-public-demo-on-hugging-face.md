---
status: accepted
date: 2026-11-14
decision-makers: Sauti engineering (repository owner)
consulted: Build Prompt §3 hard constraints 1, 5, 6, 12 and 13; ADR-0008
informed: PI, Data Protection Officer, MVP presentation audience
---
# ADR-0039: A public demo on a Hugging Face Space, with synthetic data only

## Context and Problem Statement

The MVP presentation needs a demo that anyone in the room can open: a caller reports an emergency from a basic phone, responders are alerted and accept, and the dispatch board and the audit ledger show what happened. Sauti has no hosting in Uganda before P0, so the demo would run on a Hugging Face Space (Docker SDK), which is hosted outside Uganda and open to the internet.

Two hard constraints of the Build Prompt (§3) forbid that for real data: personal data stays in Uganda (1), and there is no access from outside Uganda (13). Can Sauti show a working system outside Uganda at all, and if so, what makes that safe?

## Decision Drivers

* Hard constraints 1 and 13 protect real callers, responders and their data; the demo must hold none.
* A public web page invites people to type real names, numbers and symptoms (hard constraints 5, 6 and 12).
* A demo image must never become a way to reach real systems: providers, stores or keys.
* The confidential governing documents never leave the repository owner's machine (ADR-0008).
* The demo should run the real services, not a mock-up, so that it shows the real behaviour.

## Considered Options

* **A Hugging Face Space running the real services on synthetic, throwaway data that connects to nothing real.**
* Wait for the P0 hosting in Uganda: no demo for the MVP presentation.
* A recorded video or a laptop-only demo: safe, but the audience cannot try it.

## Decision Outcome

Chosen option: **a Hugging Face Space with synthetic data only**. It is acceptable only because the Space holds synthetic, throwaway data and connects to no real system. It is not a deployment of Sauti and is never evidence that hard constraints 1 or 13 are met. The conditions below are part of the decision. Removing one needs a new ADR.

1. **Synthetic data only.** The responders carry role labels ("VHT 1", "Transporter 2"), the numbers come from the test ranges, and the places come from the placeholder gazetteer. A permanent banner says: "Demo — synthetic data only. Do not enter real names, phone numbers or health information. Everything is erased on restart." The phone emulators send only the demo's own numbers. A reply that holds any other phone number is refused before it reaches intake-gw.
2. **Nothing persists.** PostgreSQL, the development keys and the checkpoint witness live in one temporary directory per run. The runner deletes it on shutdown and on Reset. The Space has no persistent storage. Every password, token and key is random at every start, and none is committed.
3. **No real system.** `cmd/demo` refuses `SAUTI_ENV=production` and refuses any provider credential or endpoint (`SAUTI_AT_*`, `SAUTI_ARI_*`) or store DSN in its environment. It gives its child processes a fresh environment. The SMS adapter points at a fake on loopback, which the runner checks, and the egress allowlist holds only 127.0.0.1. Voice is not wired.
4. **The caller's number is never shown.** The board reads with `op_readonly`, which cannot read a number, and masks any number in a payload again.
5. **Confidential documents never enter the image.** `.dockerignore` excludes them. `scripts/deploy/hf-space-stage.sh` builds the upload from the committed tree and refuses if any PDF, any `sa/` directory or any file over 5 MiB is staged.
6. **The PENDING texts are shown as they are.** The demo never writes clinical wording of its own.

### Consequences

* Good: the audience can use the real intake, dispatch and audit services from a browser, with nothing to install.
* Good: `cmd/demo` also runs the whole system locally in one command, without Docker.
* Bad: the Space is public. Someone can still type a real name or symptom into a free-text screen. The banner warns them, the number guard covers phone numbers, and a restart or Reset erases it all.
* Bad: the image is built and tested only on the Space. Docker is not part of CI (see the runbook's list of image-only risks).
* Neutral: the demo shows the protocol mode, the placeholder copy and the placeholder gazetteer of the configuration in force, never a configuration of its own.

### Confirmation

- `cmd/demo/main_test.go::TestRefusesProductionAndRealProviders`, `::TestRefusesRealLookingNumbers`, `::TestBoardMasksNumbers`, `::TestSinkSpeaksTheProviderContract`, `::TestDockerignoreKeepsConfidentialFilesOut`, `::TestStagedSpaceHoldsNoConfidentialFiles`
- `cmd/demo/main_integration_test.go::TestDemoScenario1OverHTTP` (scenario 1 over HTTP; the caller's number appears in no response; Reset erases; SIGTERM leaves no data directory)
- Threats T-032 and T-033 in `threat-model/threats.yaml`; the deploy checklist in `docs/runbooks/demo-hf-space.md`.
- The repository owner reviews the staged tree before every upload.
