# Tutorial: run the Sauti Dispatch prototype on your own computer

**Type:** Tutorial (Diátaxis) · 20–30 minutes the first time, about 1 minute after that · Linux x86-64, verified. macOS and Windows (WSL2) are untested; see step 1.

This tutorial is for anyone presenting or trying the prototype. By the end you will have:

- started the whole system on your computer with one command;
- reported an emergency from a simulated basic phone, and from a simulated smartphone that fills in the place from its location;
- had a VHT and a transporter accept it, and followed the transporter to the health facility;
- watched the live queue, the map and the activity feed follow the incident;
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
  - three phones side by side: the caller, a VHT and a transporter, each one a basic phone or a smartphone at the press of a switch;
  - live figures, an activity feed and a map of the wards;
  - buttons that play scenarios on the phones, and a responder autopilot;
  - the live queue of incidents, most urgent first, with the selected incident in full;
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

Open **http://127.0.0.1:7860** in a browser. **Verify audit ledger** and **Reset demo** sit at the top right. Then, from top to bottom:

- **Five live figures:** open incidents by severity, the median time from call to acceptance (T1 to T4), alerts by outcome, escalations, and SMS sent. All read 0 for now.
- **Play a scenario:** six buttons that play a whole scenario on the phones, and the **Responder autopilot** beside them. Step 8 uses both.
- **The phones**, side by side on a wide screen:
  - **Caller**, with tabs for **Caller A** and **Caller B**;
  - **VHT**, with tabs for VHT 1 to VHT 4;
  - **Transporter**, with tabs for Transporters 1 to 3.

  Test Ward 1 has VHTs 1 to 3 and Transporters 1 and 2. Test Ward 2 has VHT 4 and Transporter 3. Test Wards 3 and 4 have nobody. A tab says what that person has right now: **Alert 1:12** while an alert waits for them (the time left to answer), **Open alert** once that time has passed but nobody has taken the role, **On a job** after they accept, or **2 new** for unread SMS.

  The switch at the top right of each phone, **Basic phone | Smartphone**, changes that phone:
  - a **basic phone** has a screen, a **Dial** button for the service code, a keypad, a reply box with a green call or reply button, and **End**;
  - a **smartphone** runs the Sauti app. The app sends the same keys to the same USSD service, so the system behind it is the same. Location, notifications and the steps after accepting are what a smartphone adds; see step 7.

  Every phone has its **SMS inbox** under it, with **Clear** on the right of its heading. Clear empties that phone's inbox on this page only, as deleting messages on a handset would: the figures and the activity feed still count the messages. The page remembers each switch in this browser.
- **Activity:** every event and SMS, newest first. Each line names the service that produced it: `intake-gw`, `dispatch-worker`, `outbox-relay`, `SMS`, or `responder app` for the steps recorded from a smartphone.
- **Live queue:** every incident, most urgent first: red before yellow before green, then the longest wait. The buttons above it show **All**, **Waiting**, **Assigned**, **Escalated** or **Done**, each with its count. Each row shows the severity, the incident number, what happened and where, its state, who is coming for each role, and the time since the call, in red once nobody has accepted for 3 minutes. Select a row to see that incident in full beside the list: its timeline, the T1 to T7 times, every alert wave, the escalations and the event log. It reads "No incidents yet" until a call comes in.
- **Analytics:** two charts of the protocol timestamps the study analyses. **Median time in each stage** shows, for each stage from the call (T1) to the facility (T7), the median over the incidents that completed it, with how many did. **Each incident, stage by stage** shows the latest calls on one axis from the call, one colour per stage; a faded stage is still running. Point at or Tab to a bar for its figures, or open **Show the figures as a table**.
- **Map of the wards.** The four placeholder wards fill the map. Each ward is the area nearer its centre than any other ward's. Landmarks are small dots. The registered health facilities are green squares with a white cross. A responder is a square (`V1` is VHT 1, `T2` is Transporter 2):
  - filled blue where their location is known: at a smartphone's location, or at the place a VHT on a basic phone sent on `*384*3#` (scenario D);
  - white, grouped under their ward's name, when they are on a basic phone and their location is not known.

  Incidents at the same landmark fan out around it, each joined to it by a thin line. A key beside the map explains the marks.

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
   Transporter 2 then gets `PENDING-SMS Sauti 284 262 7491: no longer needed. Thank you.` The live queue shows the incident as **Help assigned**, with VHT 1 and Transporter 1 accepted and the other three alerts stood down. On the map, the two acceptances are solid green lines and the stood-down alerts thin grey ones. In the incident's detail, **Wave 1** lists each alerted responder with their outcome and how long it took, and the timeline bar shows the intervals from the call to the acceptance. **Call to acceptance** at the top now shows that time.
5. **Find the incident from the map.** Click the red pin, or press Tab until it has focus and press Enter. The queue selects the incident and shows it in full. Pointing at a row lights its pin on the map.

### Scenario A on smartphones: location, notifications and the trip to the facility (about 3 minutes)

Press **Reset demo**, then set all three phones to **Smartphone**.

1. **The caller reports.** On **Caller A**, the home screen shows **Your location: Near Test Clinic 1, Test Ward 1**. That is the phone's GPS, simulated in the demo; **Move my location** moves it next to another place on the map. Tap **Report an emergency**, then answer each question with its button: English, Yes, Injury or accident, 18 to 59, No. At **Where is the emergency?** the app shows your location on a map: tap **Use this location**. The app answers the ward and the nearest place for you, through the same USSD screens. Tap **This number**. The app shows **Help is coming**, the incident number, and a tracker that follows the incident.
2. **The VHT is notified.** On **VHT 1** a notification drops in, and the app shows the alert: the severity, what happened, where, the distance and an estimated time from the VHT's location (simulated GPS, starting by their registered stage), the time left to answer, and a map with the route. Tap **Accept**. The app shows **Go to the patient** with the route, the caller as "Sauti bridge line" (never their number), and whether a transporter is coming. It also sends the transporter the VHT's location, health facility and number by itself (scenario D). Tap **I've arrived**: T5 is recorded.
3. **The transporter answers late.** Wait until **Transporter 1**'s tab reads **Open alert**: the 90 seconds to answer have passed, but nobody has taken the transporter's role. The app says **You can still accept**. Tap **Accept**: the acceptance counts. (Before this change, a lapsed alert disappeared from the menu and nobody could accept it; see UX audit F-26.)
4. **The trip.** On Transporter 1 tap **I've arrived**, then **Patient picked up, leaving now** (T6). The app lists the facilities by distance and shows the route to the nearest. Tap **Arrived at the facility** (T7). The incident closes, both responders are free for the next call, and the queue moves it to **Done**. The caller's tracker shows every step done, and names the facility the patient was taken to.

Arrival, departure and arrival at the facility are a preview of milestone M3: no service records them yet, so the demo writes them through the single write path, as the M0 demo does. A basic phone records them too, on `*384*3#` (scenario D). A coordinator can also close an incident from its detail in the queue with **Close incident**, a stand-in for the M5 console. Alerts still waiting for an answer are stood down first, and those responders get the "no longer needed" SMS.

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

### Scenario D: a pregnancy emergency, and the VHT and the rider told about each other (about 3 minutes)

A transporter (a boda-boda rider) cannot know where the VHT is. Once both have accepted the same incident, Sauti tells each about the other. The rider learns where the VHT is, how far that is from the patient, the health facility the VHT works at and the VHT's number. With that, the rider can decide whether to pick the VHT up on the way or go straight to the patient. The VHT learns which rider is coming and the rider's number.

Press **Reset demo**. Leave the VHT and the transporter on **Basic phone** for now.

1. **The call.** On **Caller A**, dial `*384*1#` and press `1` (English), `1` (bleeding a lot: yes), `2` (**Pregnancy or birth**), `3`, `2`, `1` (Test Ward 1), `3` (**Test School 1**), `1`. It is triaged RED. VHTs 1 to 3 and Transporters 1 and 2 are alerted.
2. **Both accept.** On **Transporter 1**, dial `*384*2#` and press `1`. On **VHT 1**, do the same.
3. **The VHT is reminded.** If VHT 1 has not sent their details 8 seconds after both accepted, their inbox gets:
   `PENDING-SMS Sauti …: Transporter 1 is coming to help. Call +256•••••••••. Dial *384*3# to send them where you are.`
4. **The VHT sends where they are.** On **VHT 1**, dial `*384*3#`, the **My incident** menu:

   | Screen | Press | Meaning |
   |---|---|---|
   | My incident …, Test School 1, Test Ward 1 | `1` | Send the transporter where I am |
   | Which health facility do you work at? | `1` | Test Health Centre 1 |
   | Is this phone's number the one to call you on? | `1` | Yes (`2` lets you type another, a demo number only) |
   | Which ward are you in? | `1` | Test Ward 1 |
   | Nearest place to you? | `4` | Test Church 1 |

   The screen ends with `PENDING-RESPONDER Sent to Transporter 1: you are near Test Church 1, Test Ward 1.`
5. **The rider is told.** Within a second, **Transporter 1**'s inbox gets:
   `PENDING-SMS Sauti …: VHT 1 is near Test Church 1, Test Ward 1, 0.3 km from the patient. Works at Test Health Centre 1. Call +256•••••••••.`
   The page shows every number masked, as it always does; the handset would show it in full. On the map, VHT 1 is now a filled square near Test Church 1.
6. **The job, on basic phones.** On `*384*3#`, VHT 1 presses `2` (**I have arrived**). Transporter 1 dials it three times and presses `1` each time: **I have arrived**, then **Patient picked up, leaving now**, then **Arrived at the facility**, where a list of facilities follows, nearest to the patient first. They press `1`, **Test Health Centre 1**, the VHT's facility. The incident closes.

**On smartphones.** Switch the VHT and the transporter to **Smartphone** and play the scenario again.
- **VHT 1's app** sends their details as soon as they accept, with no questions: their location from GPS (simulated), and the facility and number they registered with. Its **Your transporter** card names the rider coming and their number, and says what was sent. **Move my location** while on the job sends the new place, and the rider gets "VHT 1 is now near …".
- **Transporter 1's app** has a card for VHT 1 with their place, the distance to the patient, their facility and number. It also compares going straight to the patient with picking up VHT 1 first, for example `Straight to the patient: 0.1 km, about 1 min. Picking up VHT 1 first: 0.5 km, about 2 min.` Its map marks VHT 1 with a purple dot.

Any mix works: a VHT on a basic phone with a rider on a smartphone, or the other way round. A rider on a basic phone also sees `VHT 1: near Test Church 1` on their `*384*3#` menu.

This is a preview: no Sauti service records these details yet. The wording is a placeholder, the SMS go straight to the demo's SMS simulator rather than through dispatch-worker, and nothing is written to the operational store or the audit ledger.

## 8. Let the page play the scenarios

The **Play a scenario** buttons replay the scenarios above, and two more, on the same phones. They press the same keys through the same USSD service as you did, one screen at a time, so the audience sees every screen.

1. Click **Red injury, accepted**. Each scenario starts from a fresh demo, so if the board holds incidents, the page asks before it erases them. Click **OK**.
2. Watch the status line under the buttons. It reads, for example, `Red injury, accepted. Step 3 of 6: Waiting for the alerts to VHT 1, VHT 2 and Transporter 1.` The phones being dialled show a notice and their keys are locked until the scenario ends.
3. After about 20 seconds the line reads `Red injury, accepted: done.`, followed by the incident number, who accepted, and the time from call to acceptance, all read from the board.

To stop a scenario part way, click **Cancel scenario**. The six scenarios are:

| Button | What it shows | Takes about |
|---|---|---|
| **Pregnancy emergency, end to end** | Scenario D: Transporter 1 and VHT 1 accept; VHT 1 sends their place, facility and number (on `*384*3#`, or by the app on a smartphone); both arrive; the rider hands over at the VHT's facility. Follows the phone type each panel is set to | 30 s |
| **Red injury, accepted** | Scenario A: five alerts at once; VHT 1 and Transporter 1 accept | 20 s |
| **A decline cascades** | Scenario B: VHT 4 declines and a VHT in Test Ward 1 is alerted in wave 2 | 15 s |
| **Nobody nearby, escalated** | Scenario C: both roles escalate to a coordinator | 15 s |
| **A surge of five calls** | Five red calls in a row from Test Ward 2. Each responder holds one alert at a time, for the nearest call: VHT 4 takes the first, and the calls nobody is free for wait in the queue ("waiting for a free responder") | 45 s |
| **Two calls, one emergency** | Callers A and B report the same emergency; the second call merges into the first | 20 s |

Now let the responders answer by themselves:

1. Under **Responder autopilot**, tick **Answer new alerts on the responder phones**. The line under it reads `On. New alerts are answered on the responder phones after the delay.`
2. Set the sliders. Each share is the chance that a responder accepts, declines or ignores an alert; the percentages beside them always add up to 100.
3. Report an emergency on a caller phone, as in scenario A. After the delay you chose, the responder phone dials `*384*2#` by itself and presses `1` or `2`. The line reports it, for example `VHT 4 accepted 233 039 2428 on *384*2#.`

The autopilot answers each alert at most once, and only alerts sent while it is on. An ignored alert stays unanswered until dispatch marks it **no response**, 90 seconds after it was sent. The autopilot waits while a scenario plays.

## 9. Verify the audit ledger

Press **Verify audit ledger**. Its result appears above the live queue. The prototype:

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
| On Windows, `intake-gw` stops with `triage: bundle signature not valid` | Git converted the files to CRLF line endings, which changes the signed triage bundle. In the clone, run `git config core.autocrlf false`, then `git rm --cached -r .` and `git reset --hard`, and rebuild. |
| A responder's menu says "no active alert" | the alert lapsed and its role was taken by someone else, or the responder already holds a job. The tabs say who holds a live alert. A job ends when the transporter records the arrival at the facility, or when a coordinator closes the incident. |

## Where to go next

- **[The demo runbook](../runbooks/demo-hf-space.md):** run, build and deploy the prototype; its limits; operations.
- **[ADR-0039](../adr/ADR-0039-public-demo-on-hugging-face.md):** why a public demo may hold synthetic data only.
- **[Run Sauti Dispatch locally (developers)](local-development.md):** the test suites and the quality gates CI runs.
- **[The UX audit](../explanation/ux-audit-2026-11.md):** what the system does well, and what is being improved.
