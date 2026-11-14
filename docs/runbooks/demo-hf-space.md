# Runbook: The MVP demo on a Hugging Face Space

| Field | Value |
|---|---|
| Owner | repository owner (engineering) |
| Severity | SEV-4: a demo with synthetic data; no caller or responder depends on it |
| Alert / trigger | the Space shows "Runtime error", or the demo's status line says "The system did not start" or "Stopped: …" |
| Last reviewed: | 2026-11-14 |
| Last drilled | 2026-11-14 natively (`go run ./cmd/demo`); the Docker image has not been built yet |

**Type:** How-to (Diátaxis) · **Code:** `cmd/demo/`, `Dockerfile`, `.dockerignore`, `scripts/deploy/hf-space-stage.sh`, `deploy/hf-space/README.md` · **Decision:** [ADR-0039](../adr/ADR-0039-public-demo-on-hugging-face.md)

## Summary
`cmd/demo` runs Sauti Dispatch in one process tree for a presentation. It starts a private PostgreSQL 18 on loopback, provisions the five stores, roles, keys and gazetteer (`internal/platform/demo/provision.go`, shared with the M0 demo), registers synthetic responders, and launches `intake-gw`, `dispatch-worker` and `outbox-relay`. It then serves a web UI on port 7860 with a caller phone, the responders' phones, the dispatch board, the ledger verifier and Reset. The data is synthetic and throwaway. ADR-0039 lists the conditions that make hosting it outside Uganda acceptable.

What runs where:

| Piece | In the demo |
|---|---|
| USSD (Tool 1 and the responder menu) | the real `intake-gw`, driven through the Africa's Talking callback contract by `sim.USSD` |
| SMS (alerts, stand-downs, caller notices) | the real `dispatch-worker` and SMS adapter, pointed at a fake Africa's Talking endpoint on loopback that keeps each message for the phone on screen and returns a "Success" delivery report |
| Audit ledger | the real `outbox-relay`. **Verify audit ledger** signs a checkpoint (as `sautictl checkpoint`), then runs `verify-ledger` with the witness and `verify-projections`. |
| Board | read with `op_readonly`, which cannot read a number. Event payloads are masked again. |
| Responders | VHT 1–3 and Transporter 1–2 in Test Ward 1 (three VHTs, so a red call shows the parallel alerts and the first acceptance winning), VHT 4 and Transporter 3 in Test Ward 2, none in Wards 3 and 4 (a call there shows the escalation). Numbers +256772000201 to +256772000207; callers +256772123456 and +256772000777, all from the test ranges. |

## Run it locally (no Docker)
For a step-by-step version with installation help, the three presentation scenarios and troubleshooting, see the tutorial [Run the prototype on your own computer](../tutorials/run-the-prototype-locally.md).

You need PostgreSQL 18 binaries (`initdb`, `postgres`). Use a system install, such as `/usr/lib/postgresql/18/bin` on Debian, or the embedded PostgreSQL that the integration tests cache under `~/.cache/sauti/embedded-postgres/postgresql-18.6*/*/bin`. Run as an ordinary user: PostgreSQL refuses root.

```bash
go build -o bin/ ./cmd/demo ./cmd/intake-gw ./cmd/dispatch-worker ./cmd/outbox-relay
bin/demo --pg-bin /usr/lib/postgresql/18/bin --addr 127.0.0.1:7860
# or, without building the runner:
go run ./cmd/demo --pg-bin /usr/lib/postgresql/18/bin --bin-dir bin --addr 127.0.0.1:7860
```

Open http://127.0.0.1:7860. The UI answers at once and shows "Starting the system…" for a few seconds. Flags: `--data` (writable base directory, default the system temporary directory), `--bin-dir` (the three services; default the runner's own directory, then `$PATH`), `--config-dir` (default `config`) and `--rules-root` (default `.`). Ctrl-C or SIGTERM stops every child and deletes the run's `sauti-demo-*` directory. A SIGKILL or a crash skips that cleanup: delete any leftover `sauti-demo-*` directory under `--data` by hand.

The runner refuses to start when `SAUTI_ENV` is anything but `dev` or `test`, or when any `SAUTI_AT_*`, `SAUTI_ARI_*`, `SAUTI_OP_DSN` or `SAUTI_AUDIT_DSN` is set. Unset them; the demo sets its own.

## Build and run the image

```bash
docker build -t sauti-demo .
docker run --rm -p 7860:7860 --user 1000:1000 sauti-demo
```

Stage 1 builds every command with `golang:1.27.4`. The runtime image is `postgres:18.6`, pinned to the digest CI uses, with the binaries in `/app/bin`, `config/` and `triage-rules/` beside them, a data directory `/app/var`, user 1000 and port 7860. The SQL migrations are compiled into the binaries. These risks are checked only when the image is built or run, not by CI:

| Risk | Defence |
|---|---|
| PostgreSQL 18's binaries move within `postgres:18.6` (expected at `/usr/lib/postgresql/18/bin`) | the build fails if `initdb` or `postgres` is missing there or not version 18 |
| The platform runs another uid than 1000 | `/app/var` is mode 1777; each run gets its own 0700 directory |
| The base image's entrypoint or stop signal (SIGINT) wins | `ENTRYPOINT`, `CMD []` and `STOPSIGNAL SIGTERM` are set explicitly |
| `/var/run/postgresql` is not writable by uid 1000 | the server has no Unix socket (`unix_socket_directories=`) and listens on 127.0.0.1 only |
| A runtime file is missing from the image | the build fails unless the four binaries, the gazetteer and the signed triage bundle are present |
| Hugging Face frames the app in an iframe | no `frame-ancestors` or `X-Frame-Options` header is sent |
| A small `/dev/shm` on the platform | not defended: PostgreSQL's default dynamic shared memory is used. If start-up fails with a shared-memory error, see Mitigation. |

## Deploy to the Space
1. Commit everything. The staging script uploads `HEAD` only.
2. Stage the upload. The script refuses if any PDF, any `sa/` directory or any file over 5 MiB was staged.

   ```bash
   scripts/deploy/hf-space-stage.sh /tmp/sauti-space
   find /tmp/sauti-space -type f | sort | less     # review every file
   ```
3. Create the Space once. A private Space needs a Hugging Face login to view; make it public for the presentation, if you choose to.

   ```bash
   hf repo create <owner>/sauti-dispatch-demo --repo-type space --space-sdk docker --private
   ```
4. Upload. `--delete '*'` removes files that are no longer staged.

   ```bash
   hf upload <owner>/sauti-dispatch-demo /tmp/sauti-space . --repo-type space --delete '*' --commit-message "Sauti demo $(git rev-parse --short HEAD)"
   ```
5. Watch the build log on the Space page, then open the app and run the scenario in Verification.

Never add Space secrets or variables: the demo needs none and refuses provider settings. Never enable persistent storage.

## Reset
**Reset demo** in the UI erases the run (services, PostgreSQL, data, keys, witness, messages) and starts a new one with fresh secrets and the same synthetic responders. Restarting the Space does the same. Responders are available for 8 hours from the start. After that, a responder can switch back on through the menu (3 Availability, then 1), or anyone can press Reset.

## What the demo does not show
- **Voice**: the toll-free IVR line, voice alerts at 20 s, coordinator and facility calls, and the masked bridge. `ivr-controller` needs Asterisk ARI. The fake ARI (`internal/platform/sim/ari.go`) drives tests by API, and a browser telephone for it would be a project of its own. The SMS alerts, the USSD menu and the 90-second cascade work without voice.
- The coordinator console (M5), arrival and field status (M3), payments (M4), the analytic store and the pseudonymiser (M6).
- Approved clinical and message wording: every `PENDING` text is shown as the system sends it.
- Production hardening: OpenBao, TLS, the network controls and hosting in Uganda (P0).

## Triggers and detection
- The status line in the UI: "Live" (ready), "The system did not start" (start failed), or "Stopped: <service>" (a child exited).
- The container log: the runner's lines, then each child's lines prefixed `[postgres]`, `[intake-gw]`, `[dispatch-worker]` or `[outbox-relay]`. Errors never reach the public UI; they stay in the log.

## Impact
None on the study or on any real person: the demo holds only synthetic data and reaches no real system. During a presentation, a failed demo costs the live walk-through; a local run is the fallback.

## Diagnosis
1. Read the Space's container log (or the terminal of a local run) from the top.
2. `demo: refusing …`: a provider setting or `SAUTI_ENV` is set. Remove it.
3. `PostgreSQL 18 is required` or `--pg-bin … has no executable`: the binaries path is wrong.
4. `PostgreSQL refuses to run as root`: run as uid 1000 (`--user 1000:1000`).
5. `[postgres]` errors at start-up: shared memory or disk. `[intake-gw]` exiting during start-up: the configuration or triage bundle in the image failed its checks; rebuild from a commit whose CI passed.
6. Locally, `go test -tags integration -run TestDemoScenario1OverHTTP ./cmd/demo/` reproduces the whole path in under a minute.

## Mitigation
1. Press **Reset demo**. Most transient faults clear with a fresh run.
2. Restart the Space from its settings page. This also erases everything.
3. For a shared-memory failure, rebuild after adding `"-c", "dynamic_shared_memory_type=mmap"` to the PostgreSQL arguments in `cmd/demo/proc.go`.
4. Before the presentation, keep a local run ready (Run it locally).

## Escalation
The repository owner, by the team's usual chat. The demo holds no clinical matter. If someone reports that real personal data was typed into the Space, restart the Space at once (which erases it) and report it to the Data Protection Officer.

## Verification
1. The status line says "Live".
2. Caller A: dial `*384*1#`, answer 1, 1, 1, 3, 2, 1, 2, 1. The last screen says "HELP IS COMING" with an incident number.
3. Within seconds, VHT 1, VHT 2, VHT 3 and Transporters 1 and 2 have the alert SMS. VHT 1 dials `*384*2#` and presses 1: "Accepted". The other two VHTs get "no longer needed", and Caller A gets "help is on the way".
4. The board shows the incident as red, "Help assigned", with T1–T4, the alerts with "alert by sms: sent (Success)", and the event timeline.
5. **Verify audit ledger** reports "Audit ledger verified", 0 waiting for the relay, and the projections matching.
6. Automated: `cmd/demo/main_integration_test.go::TestDemoScenario1OverHTTP` runs steps 2–5 over HTTP, then Reset and SIGTERM.

## References
- [ADR-0039](../adr/ADR-0039-public-demo-on-hugging-face.md) (the conditions), [ADR-0008](../adr/ADR-0008-interim-hosting-and-confidentiality.md) (confidential documents), [ADR-0027](../adr/ADR-0027-channels-and-the-alert-ladder.md) (channels)
- Threats T-032 and T-033 in `threat-model/threats.yaml`
- [How to run the M0 milestone demo](../how-to/run-the-m0-demo.md)
