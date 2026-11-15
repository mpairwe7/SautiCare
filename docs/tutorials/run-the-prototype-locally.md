# Tutorial: run the Sauti Dispatch prototype on your own computer

**Type:** Tutorial (Diátaxis) · 20–30 minutes the first time, about 1 minute after that · Linux x86-64, verified. macOS and Windows (WSL2) are untested; see step 1.

This tutorial is for anyone presenting or trying the prototype. By the end you will have:

- started the whole system on your computer with one command;
- reported an emergency from a simulated basic phone;
- had a responder accept it on another simulated phone;
- watched the dispatch board follow the incident;
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
  - nine responder phones;
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

Open **http://127.0.0.1:7860** in a browser. The page has a banner and three numbered panels:

1. **Caller phone.** Tabs for **Caller A** and **Caller B**. Each phone has a screen, a **Dial** button for the emergency code, a keypad, a reply box with a green call or reply button, **End**, and an **SMS inbox**.
2. **Responder phones.** One tab per responder:
   - **Test Ward 1:** VHT 1, VHT 2, VHT 3, Transporter 1, Transporter 2;
   - **Test Ward 2:** VHT 4, Transporter 3;
   - **Test Wards 3 and 4:** nobody.
3. **Dispatch board.** It reads "No incidents yet" until a call comes in. At the top are **Verify audit ledger** and **Reset demo**.

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
   The board shows the incident as **RED** with its timeline.
2. **VHT 1 accepts.** Open **VHT 1** and dial `*384*2#`. The menu shows `PENDING-RESPONDER Sauti 284 262 7491: RED Injury or accident` with `1 Accept`, `2 Decline` and `3 Availability`. Press `1`:
   `PENDING-RESPONDER Accepted 284 262 7491. Go to Test Clinic 1, Test Ward 1. Caller: Sauti bridge line.`
   The responder never sees the caller's number, only the masked bridge line.
3. **The first acceptance wins, for each role.** A few seconds later, VHT 2 and VHT 3 get `PENDING-SMS Sauti 284 262 7491: no longer needed. Thank you.`, and their `*384*2#` menu shows `PENDING-RESPONDER Sauti: no active alert.` with only `3 Availability`. Caller A's inbox gets `PENDING-SMS Sauti 284 262 7491: help is on the way.` The transporters keep their alerts: a transporter accepts separately.
4. **Transporter 1 accepts.** Open **Transporter 1**, dial `*384*2#` and press `1`. The menu is the same as VHT 1's, and the answer is:
   `PENDING-RESPONDER Accepted 284 262 7491. Go to Test Clinic 1, Test Ward 1. Caller: Sauti bridge line.`
   Transporter 2 then gets `PENDING-SMS Sauti 284 262 7491: no longer needed. Thank you.` The board shows the incident as **Help assigned**, with VHT 1 and Transporter 1 accepted and the other three alerts stood down.

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

## 8. Verify the audit ledger

Press **Verify audit ledger**. The prototype:

1. signs a checkpoint;
2. checks every ledger entry against the signed checkpoint and the witness;
3. checks that the dashboard figures (the projections) match the events.

After the scenarios above it reports **Audit ledger verified**, with counts such as 33 entries, checkpoint and witness OK, and 0 drift. Every step a caller, responder or system took is in that ledger, and changing any of it would make this check fail.

## 9. Reset, stop and start again

- **Reset demo** erases everything and re-registers the responders, without restarting. Use it between audiences.
- **To stop,** press **Ctrl-C** in the terminal from step 5. The prototype stops every part within a few seconds and deletes its data.
- **To start again,** repeat step 5. You do not need to rebuild unless the code changed.

Seeded responders are marked available for 8 hours. After that, a responder switches back on through `*384*2#`, then `3`, then `1`, or you press **Reset demo**.

## 10. Optional: run it in Docker instead

If Docker is installed, you can skip steps 3–5:

```bash
docker build -t sauti-demo .
docker run --rm -p 7860:7860 sauti-demo
```

Then open http://127.0.0.1:7860. The image contains PostgreSQL 18 and runs as an ordinary user. Building it needs internet access.

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
