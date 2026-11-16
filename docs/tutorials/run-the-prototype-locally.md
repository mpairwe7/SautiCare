# Tutorial: run the Sauti Dispatch prototype on your own computer

**Type:** Tutorial (Diátaxis) · 20–30 minutes the first time, about 1 minute after that · Linux x86-64, verified. macOS and Windows (WSL2) are untested; see step 1.

This tutorial is for anyone presenting or trying the prototype. By the end you will have:

- started the whole system on your computer with one command;
- reported an emergency from a simulated basic phone;
- had a responder accept it on another simulated phone;
- watched the dispatch board, the map and the activity feed follow the incident;
- let the page play scenarios on the phones, and let the responders answer by themselves;
- verified the tamper-evident audit ledger.

The same prototype is packaged for a Hugging Face Space (see [the demo runbook](../runbooks/demo-hf-space.md)). Running it locally needs no internet once it is built, so it is a dependable fallback for a presentation.

> **Synthetic data only.** Every responder, phone number and place in the prototype is invented. Never type a real name, phone number or health detail into it. Everything is erased when it stops. Hosting rules: [ADR-0039](../adr/ADR-0039-public-demo-on-hugging-face.md).

## What runs on your computer

One command, `bin/demo`, starts all of the following on your computer only:

- a private PostgreSQL 18 database, which listens on 127.0.0.1 only and holds throwaway data;
- the real Sauti services: `intake-gw` (USSD), `dispatch-worker` (alerts) and `outbox-relay` (the audit ledger);
- a simulated SMS provider;
- a web page at http://127.0.0.1:7860 with:
  - two caller phones;
  - seven responder phones;
  - live figures, a map of the wards and an activity feed;
  - buttons that play scenarios on the phones, and a responder autopilot;
  - the dispatch board;
  - an audit-ledger check;
  - a Reset button.

Voice calls are not part of the prototype.

## 1. Check the prerequisites

| You need | Why | Check |
|---|---|---|
| Linux x86-64, or WSL2 on Windows | the verified platform | `uname -sm` prints `Linux x86_64` |
| An ordinary user account, not root | PostgreSQL refuses to run as root | `id -u` prints anything except `0` |
| Go, any recent version | builds the prototype. The repository pins Go 1.27.4, which Go downloads by itself on first use. | `go version` |
| `git` | gets the code | `git --version` |
| PostgreSQL 18 programs (`initdb`, `postgres`) | the prototype starts its own private database with them. You do **not** need a running PostgreSQL server. | step 3 |
| Port 7860 free | the web page | `ss -ltn \| grep -c ':7860 '` prints `0` |

On macOS, Homebrew's `postgresql@18` should provide the programs, but this has not been tested. On Windows, install Ubuntu in WSL2 and follow the Linux steps inside it.

## 2. Get the code

```bash
git clone https://github.com/mpairwe7/SautiCare.git
cd SautiCare
```

Run every command below from this `SautiCare` directory. The prototype reads `config/` and `triage-rules/` from where you start it.

## 3. Get the PostgreSQL 18 programs

Choose **one** option.

**Option A: let the repository download them** (Linux x86-64, about 30 MB, no administrator rights). The integration tests download PostgreSQL 18.6 into a cache the first time they run:

```bash
go test -tags integration -count=1 ./tests/e2e/
PG_BIN=$(ls -d ~/.cache/sauti/embedded-postgres/postgresql-18.6*/*/bin | head -1)
```

**Option B: install them from the PostgreSQL project's packages** (Debian or Ubuntu, needs `sudo`):

```bash
sudo apt install -y postgresql-common
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh
sudo apt install -y postgresql-18
PG_BIN=/usr/lib/postgresql/18/bin
```

The package also starts a system database on port 5432. The prototype does not use it. Stop it with `sudo systemctl disable --now postgresql` if you do not want it.

Either way, check that you have version 18:

```bash
"$PG_BIN/postgres" --version     # postgres (PostgreSQL) 18.x
```

## 4. Build the prototype

```bash
go build -o bin/ ./cmd/demo ./cmd/intake-gw ./cmd/dispatch-worker ./cmd/outbox-relay
```

This puts four programs in `bin/`. The first build downloads Go modules and can take a few minutes; later builds take seconds. Rebuild after you pull new code.

## 5. Start it

```bash
bin/demo --pg-bin "$PG_BIN" --addr 127.0.0.1:7860
```

Log lines from each part follow, ending with:

```text
[postgres] ... LOG:  database system is ready to accept connections
[outbox-relay] {... "msg":"outbox-relay is the active leader"}
[dispatch-worker] {... "msg":"dispatch-worker starting", ...}
[intake-gw] {... "msg":"intake-gw listening", ...}
time=... level=INFO msg="demo ready"
```

`demo ready` usually appears within a few seconds. Leave this terminal open: the prototype runs only while this command runs.

## 6. Open the page

Open **http://127.0.0.1:7860** in a browser. Under the banner, **Verify audit ledger** and **Reset demo** sit at the top right. Then, from top to bottom:

- **Five live figures:** open incidents by severity, the median time from call to acceptance (T1 to T4), alerts by outcome, escalations, and SMS sent. All read 0 for now.
- **Play a scenario:** five buttons that play a whole scenario on the phones, and the **Responder autopilot** beside them. Step 8 uses both.
- **1. Caller phone.** Tabs for **Caller A** and **Caller B**. Each phone has a screen, a **Dial** button for the emergency code, a keypad, a reply box with a green call or reply button, **End**, and an **SMS inbox**.
- **2. Responder phones.** One tab per responder:
  - **Test Ward 1:** VHT 1, VHT 2, VHT 3, Transporter 1, Transporter 2;
  - **Test Ward 2:** VHT 4, Transporter 3;
  - **Test Wards 3 and 4:** nobody.
- **Map of the wards.** The four placeholder wards and their landmarks. Each responder is a small square in their ward (`V1` is VHT 1, `T2` is Transporter 2). A key under the map explains the marks.
- **Activity.** Every event and SMS, newest first, each tagged with the service that produced it: `intake-gw`, `dispatch-worker`, `outbox-relay` or `SMS`.
- **3. Dispatch board.** It reads "No incidents yet" until a call comes in.

Phone numbers are always shown masked (`+256•••••••••`). That is deliberate: the system never reveals a caller's number.

Screens that start with `PENDING-` are placeholders for wording the study has not approved yet. The prototype shows them exactly as the system sends them. They are expected; nothing is broken.

## 7. Walk through three scenarios

To answer a screen, type the number into the reply box and press Enter (or the green button). You can also click the keypad.

### Scenario A: a red emergency with responders nearby (about 2 minutes)

On **Caller A**, dial `*384*1#`, then answer:

| Screen | Press | Meaning |
|---|---|---|
| Sauti emergency help (language) | `1` | English |
| Bleeding a lot, unconscious or not breathing well? | `1` | Yes |
| What happened? | `1` | Injury or accident |
| Age of the person? | `3` | 18 to 59 |
| Trauma danger question | `2` | No |
| Which ward? | `1` | Test Ward 1 |
| Nearest place? | `2` | Test Clinic 1 |
| Number to call you back on? | `1` | This number |

The phone ends the session with:

```text
HELP IS COMING
Incident 284 262 7491
PENDING-TOOL1 first aid line
```

Your incident number will differ.

Then:

1. **The responders get the alert.** Within a few seconds, all five Test Ward 1 responders (VHT 1 to 3 and Transporters 1 and 2) have this SMS:
   `PENDING-SMS Sauti 284 262 7491: RED Injury or accident. Dial *384*2# to accept or decline.`
   The board shows the incident as **RED** with its timeline. On the map, a red pin marked `R` appears at Test Clinic 1, with a dashed blue line from each of the five responders: their alerts wait for an answer. The **Activity** feed lists each step, such as `intake-gw` "Triaged RED by rule red-danger-sign" and `dispatch-worker` "Alert to VHT 1: ring 1, wave 1", and notes when `outbox-relay` sealed each event into the audit ledger.
2. **VHT 1 accepts.** Open **VHT 1** and dial `*384*2#`. The menu shows `PENDING-RESPONDER Sauti 284 262 7491: RED Injury or accident` with `1 Accept`, `2 Decline` and `3 Availability`. Press `1`:
   `PENDING-RESPONDER Accepted 284 262 7491. Go to Test Clinic 1, Test Ward 1. Caller: Sauti bridge line.`
   The responder never sees the caller's number, only the masked bridge line.
3. **The first acceptance wins, for each role.** A few seconds later, VHT 2 and VHT 3 get `PENDING-SMS Sauti 284 262 7491: no longer needed. Thank you.`, and their `*384*2#` menu shows `PENDING-RESPONDER Sauti: no active alert.` with only `3 Availability`. Caller A's inbox gets `PENDING-SMS Sauti 284 262 7491: help is on the way.` The transporters keep their alerts: a transporter accepts separately.
4. **Transporter 1 accepts.** Open **Transporter 1**, dial `*384*2#` and press `1`. The menu is the same as VHT 1's, and the answer is:
   `PENDING-RESPONDER Accepted 284 262 7491. Go to Test Clinic 1, Test Ward 1. Caller: Sauti bridge line.`
   Transporter 2 then gets `PENDING-SMS Sauti 284 262 7491: no longer needed. Thank you.` The board shows the incident as **Help assigned**, with VHT 1 and Transporter 1 accepted and the other three alerts stood down. On the map, the two acceptances are solid green lines and the stood-down alerts thin grey ones. In the card, **Wave 1** lists each alerted responder with their outcome and how long it took, and the timeline bar shows the intervals from the call to the acceptance. **Call to acceptance** at the top now shows that time.
5. **Find the incident from the map.** Click the red pin, or press Tab until it has focus and press Enter. The page scrolls to the incident's card and outlines it. Pointing at a card lights its pin on the map.

### Scenario B: a responder declines (about 1 minute)

On **Caller B**, dial `*384*1#` and press `1`, `1`, `1`, `3`, `2`, then `2` (**Test Ward 2**), `1`, `1`. VHT 4 and Transporter 3 get the alert.

On **VHT 4**, dial `*384*2#` and press `2`. The screen shows `PENDING-RESPONDER Declined … Thank you.` The board marks VHT 4 **declined**, and dispatch goes on to alert more responders in its list.

Pressing `3` on a responder menu shows the availability screen instead: `You are available until …`, with `1 Available for 8 hours` and `2 Off duty`.

### Scenario C: nobody nearby, so an escalation is recorded (about 1 minute)

Press **Reset demo** first, if you like. On **Caller B**, dial `*384*1#`, then answer:

| Press | Meaning |
|---|---|
| `1` | English |
| `2` | No danger signs |
| `1` | Injury or accident |
| `3` | 18 to 59 |
| `2` | No |
| `3` | **Test Ward 3**, which has no responders |
| `1` | Test Market 3 |
| `1` | This number |

The caller still sees `HELP IS COMING` with an incident number. No responder can be alerted, so within a few seconds the board marks the incident (GREEN) **Escalated to a coordinator** and lists two escalations, one for each role, with the reason "no eligible responder".

The demo records the escalation but calls nobody: voice is not part of it. In production, each recorded escalation makes ivr-controller call a coordinator ([ADR-0034](../adr/ADR-0034-every-failure-path-ends-with-a-person.md)).

## 8. Let the page play the scenarios

The **Play a scenario** buttons replay the scenarios above, and two more, on the same phones. They press the same keys through the same USSD service as you did, one screen at a time, so the audience sees every screen.

1. Click **Red injury, accepted**. Each scenario starts from a fresh demo, so if the board holds incidents, the page asks before it erases them. Click **OK**.
2. Watch the status line under the buttons. It reads, for example, `Red injury, accepted. Step 3 of 6: Waiting for the alerts to VHT 1, VHT 2 and Transporter 1.` The phones being dialled show a notice and their keys are locked until the scenario ends.
3. After about 20 seconds the line reads `Red injury, accepted: done.`, followed by the incident number, who accepted, and the time from call to acceptance, all read from the board.

To stop a scenario part way, click **Cancel scenario**. The five scenarios are:

| Button | What it shows | Takes about |
|---|---|---|
| **Red injury, accepted** | Scenario A: five alerts at once; VHT 1 and Transporter 1 accept | 20 s |
| **A decline cascades** | Scenario B: VHT 4 declines and a VHT in Test Ward 1 is alerted in wave 2 | 15 s |
| **Nobody nearby, escalated** | Scenario C: both roles escalate to a coordinator | 15 s |
| **A surge of five calls** | Five red calls in a row from Test Ward 2 while VHT 4 accepts one; every call is dispatched | 45 s |
| **Two calls, one emergency** | Callers A and B report the same emergency; the second call merges into the first | 20 s |

Now let the responders answer by themselves:

1. Under **Responder autopilot**, tick **Answer new alerts on the responder phones**. The line under it reads `On. New alerts are answered on the responder phones after the delay.`
2. Set the sliders. Each share is the chance that a responder accepts, declines or ignores an alert; the percentages beside them always add up to 100.
3. Report an emergency on a caller phone, as in scenario A. After the delay you chose, the responder phone dials `*384*2#` by itself and presses `1` or `2`. The line reports it, for example `VHT 4 accepted 233 039 2428 on *384*2#.`

The autopilot answers each alert at most once, and only alerts sent while it is on. An ignored alert stays unanswered until dispatch marks it **no response**, 90 seconds after it was sent. The autopilot waits while a scenario plays.

## 9. Verify the audit ledger

Press **Verify audit ledger**. The prototype:

1. signs a checkpoint;
2. checks every ledger entry against the signed checkpoint and the witness;
3. checks that the dashboard figures (the projections) match the events.

After the scenarios above it reports **Audit ledger verified**, with counts such as 33 entries, checkpoint and witness OK, and 0 drift. Every step a caller, responder or system took is in that ledger, and changing any of it would make this check fail.

## 10. Reset, stop and start again

- **Reset demo** erases everything and re-registers the responders, without restarting. Use it between audiences.
- **To stop,** press **Ctrl-C** in the terminal from step 5. The prototype stops every part within a few seconds and deletes its data.
- **To start again,** repeat step 5. You do not need to rebuild unless the code changed.

Seeded responders are marked available for 8 hours. After that, a responder switches back on through `*384*2#`, then `3`, then `1`, or you press **Reset demo**.

## 11. Optional: run it in a container instead

If Docker with Docker Compose is installed, you can skip steps 3–5. From the `SautiCare` directory:

```bash
docker compose up --build
```

Then open http://127.0.0.1:7860. `compose.yaml` builds the image from your checkout and publishes the port on 127.0.0.1 only. The image contains PostgreSQL 18 and runs as an ordinary user. Building it needs internet access. Press **Ctrl-C** to stop, or run `docker compose down` if you started it in the background with `-d`. Either way, the data is deleted.

Without Compose:

```bash
docker build -t sauti-demo .
docker run --rm -p 127.0.0.1:7860:7860 sauti-demo
```

With rootless Podman, if the build cannot download modules or the page never opens, and the output mentions `slirp4netns`, Podman cannot use its own network on that machine. Build and run on the host's network instead:

```bash
docker compose -f compose.yaml -f compose.host-network.yaml up --build
```

Podman 4.3 has no `docker compose` command of its own. Use the Docker Compose binary with Podman's API socket. Export `DOCKER_HOST` so that Compose inherits it; a plain assignment without `export` stays in your shell, and Compose then looks for Docker's socket instead:

```bash
systemctl --user enable --now podman.socket
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/podman/podman.sock"
docker-compose -f compose.yaml -f compose.host-network.yaml up --build
```

Without the buildx plugin, Compose builds through Podman's Docker-compatible build API.

## Troubleshooting

| You see | Do this |
|---|---|
| `PostgreSQL refuses to run as root`, or the runner refuses root | run as an ordinary user |
| `--pg-bin … has no executable`, or `PostgreSQL 18 is required` | `PG_BIN` is wrong or empty. Redo step 3, and check `"$PG_BIN/postgres" --version`. |
| `address already in use` | choose another port, for example `--addr 127.0.0.1:8080`, and open that port instead |
| The runner refuses to start because of `SAUTI_ENV`, `SAUTI_AT_*`, `SAUTI_ARI_*`, `SAUTI_OP_DSN` or `SAUTI_AUDIT_DSN` | these come from a development `.env`. Open a fresh terminal, or `unset` them. The prototype never uses real providers or databases. |
| `config` or `triage-rules` not found | start it from the `SautiCare` directory, or pass `--config-dir <repo>/config --rules-root <repo>` |
| The page says "The system did not start" | read the terminal output above it; the first error line names the part that failed |
| A shared-memory error from PostgreSQL | the machine's `/dev/shm` is too small; see Mitigation in [the demo runbook](../runbooks/demo-hf-space.md) |
| A `sauti-demo-*` directory left in `/tmp` after a crash or `kill -9` | the prototype was stopped before it could clean up. Delete that directory. Ctrl-C always cleans up. |

## Where to go next

- **[The demo runbook](../runbooks/demo-hf-space.md):** run, build and deploy the prototype; its limits; operations.
- **[ADR-0039](../adr/ADR-0039-public-demo-on-hugging-face.md):** why a public demo may hold synthetic data only.
- **[Run Sauti Dispatch locally (developers)](local-development.md):** the test suites and the quality gates CI runs.
- **[The UX audit](../explanation/ux-audit-2026-11.md):** what the system does well, and what is being improved.
