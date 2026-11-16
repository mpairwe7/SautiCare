// Scenarios: a table of steps that one runner interprets, through the same
// USSD API the on-screen phones use. The input paths and board conditions
// are cmd/demo/edge_integration_test.go's. No DOM here: the runner's
// effects arrive as deps.
import { REF, mmss } from "./format.js";

// path is Tool 1's eight answers: English, danger (1 yes, 2 no), the
// category, 18 to 59, no, the ward, its landmark, this number.
const path = (danger, category, ward, landmark) => ["1", danger, category, "3", "2", ward, landmark, "1"];

const byRef = (b, ref) => b.incidents.find((i) => i.reference === ref);
const accepted = (inc, role) => inc.alerts.filter((a) => a.role === role && a.outcome === "accepted").length;
const pendingFor = (inc, names) => names.every((n) => inc.alerts.some((a) => a.responder === n && a.outcome === "pending"));
// holders counts the pending alerts each responder holds on open incidents:
// one at most (one alert per responder, ADR-0044).
const holders = (b) => {
  const n = {};
  for (const i of b.incidents) {
    if (["dispatching", "assigned", "on_scene"].includes(i.state)) for (const a of i.alerts) if (a.outcome === "pending") n[a.responder] = (n[a.responder] || 0) + 1;
  }
  return n;
};
const oneEach = (b) => Object.values(holders(b)).every((k) => k <= 1);
const shareOf = (b, inc, vht) => inc && (b.shares || []).find((s) => s.incident === inc.id && s.vht === vht);
const checkOf = (b, ref) => { const i = byRef(b, ref); return i && (b.arrival || []).find((a) => a.incident === i.id); };
const didOf = (b, ref, n) => checkOf(b, ref)?.asks?.[n]?.did || "";
const hms = (t) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const list = (xs) => (xs.length < 2 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);

export const SCENARIOS = [
  {
    id: "arrival-check",
    title: "Has help arrived? The caller is asked",
    description: "Callers are asked every 30 seconds for this run. Caller B reports a red emergency in Test Ward 2 and says nobody has come: Sauti reminds the responders alerted and retries. VHT 4 and Transporter 3 accept; still nobody, so both are told the caller is waiting. Then the caller says yes, which bounds when help arrived.",
    needsReset: true,
    steps: [
      { kind: "pace", label: "Asking callers every 30 seconds, for this run", seconds: 30 },
      { kind: "dial", label: "Caller B reports a red pregnancy emergency at Test Market 2", phone: "caller-b", inputs: path("1", "2", "2", "1"), as: "ref" },
      { kind: "wait", label: "Waiting for the alerts to VHT 4 and Transporter 3", timeoutMs: 30000,
        until: (b, v) => byRef(b, v.ref) && pendingFor(byRef(b, v.ref), ["VHT 4", "Transporter 3"]) },
      { kind: "answer", label: "Caller B is asked whether help has arrived: no", phone: "caller-b", ref: "ref", yes: false, timeoutMs: 60000 },
      { kind: "wait", label: "Waiting for Sauti to remind VHT 4 and Transporter 3", timeoutMs: 15000,
        until: (b, v) => didOf(b, v.ref, 0).includes("reminded VHT 4") && didOf(b, v.ref, 0).includes("reminded Transporter 3") },
      { kind: "respond", label: "VHT 4 accepts on *384*2#", phone: "vht-4", ref: "ref", answer: "accept" },
      { kind: "respond", label: "Transporter 3 accepts on *384*2#", phone: "transporter-3", ref: "ref", answer: "accept" },
      { kind: "answer", label: "Caller B is asked again: still no", phone: "caller-b", ref: "ref", yes: false, timeoutMs: 60000 },
      { kind: "wait", label: "Waiting for VHT 4 and Transporter 3 to be told the caller is waiting", timeoutMs: 15000,
        until: (b, v) => didOf(b, v.ref, 1).includes("told VHT 4") && didOf(b, v.ref, 1).includes("told Transporter 3") },
      { kind: "answer", label: "Caller B is asked again: yes, help is here", phone: "caller-b", ref: "ref", yes: true, timeoutMs: 60000 },
      { kind: "wait", label: "Waiting for the arrival window on the board", timeoutMs: 15000, until: (b, v) => checkOf(b, v.ref)?.confirmedAt },
      { kind: "pace", label: "Asking callers every 5 minutes again", seconds: 300 },
    ],
    result: (b, v) => {
      const a = checkOf(b, v.ref);
      return `Incident ${v.ref}: Caller B was asked ${a.asks.length} times; help arrived between ${hms(a.after)} and ${hms(a.confirmedAt)}, by the caller's answers. Callers are asked every 5 minutes again.`;
    },
  },
  {
    id: "pregnancy",
    title: "Pregnancy emergency, end to end",
    description: "Caller A reports a pregnancy emergency with heavy bleeding at Test School 1. Transporter 1 accepts, then VHT 1, who sends the rider where they are, their health facility and their number. Both arrive; the rider takes the mother to the VHT's facility.",
    needsReset: true,
    steps: [
      { kind: "dial", label: "Caller A reports a red pregnancy emergency at Test School 1", phone: "caller-a", inputs: path("1", "2", "1", "3"), as: "ref" },
      { kind: "wait", label: "Waiting for the alerts to VHT 1 and Transporter 1", timeoutMs: 30000,
        until: (b, v) => byRef(b, v.ref) && pendingFor(byRef(b, v.ref), ["VHT 1", "Transporter 1"]) },
      { kind: "respond", label: "Transporter 1 accepts on *384*2#", phone: "transporter-1", ref: "ref", answer: "accept" },
      { kind: "respond", label: "VHT 1 accepts on *384*2#", phone: "vht-1", ref: "ref", answer: "accept" },
      // A basic phone answers *384*3#: send it, Test Health Centre 1, this
      // number, Test Ward 1, Test Church 1. A smartphone sends by itself.
      { kind: "share", label: "VHT 1 sends Transporter 1 where they are, their facility and number", phone: "vht-1", vht: "VHT 1", ref: "ref", keys: ["1", "1", "1", "1", "4"] },
      { kind: "wait", label: "Waiting for Transporter 1's SMS with VHT 1's place, facility and number", timeoutMs: 30000,
        until: (b, v) => shareOf(b, byRef(b, v.ref), "VHT 1")?.told },
      { kind: "step", label: "VHT 1 records the arrival", phone: "vht-1", role: "vht", ref: "ref", step: "arrived" },
      { kind: "step", label: "Transporter 1 records the arrival", phone: "transporter-1", role: "transporter", ref: "ref", step: "arrived" },
      { kind: "step", label: "Transporter 1 leaves with the mother", phone: "transporter-1", role: "transporter", ref: "ref", step: "departed" },
      { kind: "step", label: "Transporter 1 hands over at VHT 1's facility", phone: "transporter-1", role: "transporter", ref: "ref", step: "at_facility", facilityOf: "VHT 1" },
      { kind: "wait", label: "Waiting for the incident to close", timeoutMs: 30000, until: (b, v) => byRef(b, v.ref)?.state === "closed" },
    ],
    result: (b, v) => {
      const i = byRef(b, v.ref);
      const s = shareOf(b, i, "VHT 1");
      const t = (k) => Date.parse(i.t[k]);
      return `Incident ${v.ref}: Transporter 1 was told VHT 1 is near ${s.landmark}${s.km !== undefined ? `, ${s.km} km from the patient` : ""}, works at ${s.facility}, with VHT 1's number (${s.via === "app" ? "from VHT 1's smartphone" : "from *384*3#"}); handed over at ${s.facility} ${mmss(t("T7") - t("T1"))} after the call.`;
    },
  },
  {
    id: "red-accepted",
    title: "Red injury, accepted",
    description: "Caller A reports a red injury at Test Clinic 1. Three VHTs and both transporters are alerted at once; VHT 1 and Transporter 1 accept.",
    needsReset: true,
    steps: [
      { kind: "dial", label: "Caller A reports a red injury in Test Ward 1", phone: "caller-a", inputs: path("1", "1", "1", "2"), as: "ref" },
      { kind: "wait", label: "Waiting for the alerts to VHT 1, VHT 2 and Transporter 1", timeoutMs: 30000,
        until: (b, v) => byRef(b, v.ref) && pendingFor(byRef(b, v.ref), ["VHT 1", "VHT 2", "Transporter 1"]) },
      { kind: "respond", label: "VHT 1 accepts on *384*2#", phone: "vht-1", ref: "ref", answer: "accept" },
      { kind: "respond", label: "Transporter 1 accepts on *384*2#", phone: "transporter-1", ref: "ref", answer: "accept" },
      { kind: "wait", label: "Waiting for one acceptance per role on the board", timeoutMs: 30000,
        until: (b, v) => { const i = byRef(b, v.ref); return i && i.state === "assigned" && accepted(i, "vht") === 1 && accepted(i, "transporter") === 1; } },
    ],
    result: (b, v) => {
      const i = byRef(b, v.ref);
      const down = i.alerts.filter((a) => a.outcome === "stood_down").length;
      return `Incident ${v.ref}: VHT 1 and Transporter 1 accepted, ${down} other alert(s) stood down, call to acceptance ${mmss(Date.parse(i.t.T4) - Date.parse(i.t.T1))}.`;
    },
  },
  {
    id: "decline-cascades",
    title: "A decline cascades",
    description: "Caller B reports a green injury in Test Ward 2. VHT 4 declines, and dispatch alerts another VHT in the next wave.",
    needsReset: true,
    steps: [
      { kind: "dial", label: "Caller B reports a green injury in Test Ward 2", phone: "caller-b", inputs: path("2", "1", "2", "1"), as: "ref" },
      { kind: "wait", label: "Waiting for the alert to VHT 4", timeoutMs: 30000, until: (b, v) => byRef(b, v.ref) && pendingFor(byRef(b, v.ref), ["VHT 4"]) },
      { kind: "respond", label: "VHT 4 declines on *384*2#", phone: "vht-4", ref: "ref", answer: "decline" },
      { kind: "wait", label: "Waiting for a further VHT in a later wave", timeoutMs: 60000,
        until: (b, v) => {
          const i = byRef(b, v.ref);
          return i && i.alerts.some((a) => a.responder === "VHT 4" && a.outcome === "declined") &&
            i.alerts.some((a) => a.role === "vht" && a.responder !== "VHT 4" && a.wave > 1);
        } },
    ],
    result: (b, v) => {
      const next = byRef(b, v.ref).alerts.find((a) => a.role === "vht" && a.responder !== "VHT 4" && a.wave > 1);
      return `Incident ${v.ref}: VHT 4 declined, so dispatch alerted ${next.responder} in wave ${next.wave}.`;
    },
  },
  {
    id: "nobody-nearby",
    title: "Nobody nearby, escalated",
    description: "Caller B reports from Test Ward 3, where no responder is registered. Each role escalates to a coordinator at once.",
    needsReset: true,
    steps: [
      { kind: "dial", label: "Caller B reports a green injury in Test Ward 3", phone: "caller-b", inputs: path("2", "1", "3", "1"), as: "ref" },
      { kind: "wait", label: "Waiting for both roles to escalate", timeoutMs: 60000,
        until: (b, v) => {
          const i = byRef(b, v.ref);
          const roles = new Set((i?.escalations || []).map((e) => e.role));
          return i && i.escalated && roles.has("vht") && roles.has("transporter");
        } },
    ],
    result: (b, v) => {
      const i = byRef(b, v.ref);
      return `Incident ${v.ref} in ${i.ward}: ${i.alerts.length} responders alerted; escalated to a coordinator for ${list(i.escalations.map((e) => e.role))}.`;
    },
  },
  {
    id: "surge",
    title: "A surge of five calls",
    description: "Five red calls from Test Ward 2 in quick succession. Each responder holds one alert at a time, for the nearest call: VHT 4 takes the first, and the calls nobody can take yet wait for the next free responder. None is dropped.",
    needsReset: true,
    steps: [
      { kind: "parallel", label: "Five calls arrive while VHT 4 answers", branches: [
        ["1", "2", "3", "4", "5"].map((cat, i) => ({ kind: "dial", label: `Call ${i + 1} of 5`, phone: i % 2 ? "caller-b" : "caller-a",
          inputs: path("1", cat, "2", "1"), as: "ref" + (i + 1) })),
        [
          { kind: "wait", label: "Waiting for VHT 4's alert, for the first call", timeoutMs: 30000, until: (b, v) => v.ref1 && byRef(b, v.ref1) && pendingFor(byRef(b, v.ref1), ["VHT 4"]) },
          { kind: "respond", label: "VHT 4 accepts the one alert it holds", phone: "vht-4", answer: "accept", as: "taken" },
        ],
      ] },
      { kind: "wait", label: "Waiting for every call to be in the queue, each responder holding one alert at most", timeoutMs: 60000,
        until: (b, v) => oneEach(b) && [1, 2, 3, 4, 5].every((n) => {
          const i = byRef(b, v["ref" + n]);
          return i && i.severity === "red" && !i.mergedInto && i.state !== "intake" && i.state !== "triaged";
        }) },
    ],
    result: (b, v) => {
      const refs = [1, 2, 3, 4, 5].map((n) => v["ref" + n]);
      const incs = refs.map((r) => byRef(b, r));
      const out = incs.filter((i) => i.alerts.some((a) => a.outcome === "pending")).length;
      const waiting = incs.filter((i) => i.state === "dispatching" && !i.alerts.some((a) => a.outcome === "pending" || a.outcome === "accepted")).length;
      const escalated = incs.filter((i) => i.escalated).length;
      return `${new Set(refs).size} calls, ${new Set(refs).size} incidents, none dropped: VHT 4 took ${v.taken}; ${waiting} waiting for a free responder, ${out} with an alert out, ${escalated} escalated; ${oneEach(b) ? "no responder held two alerts" : "A RESPONDER HELD TWO ALERTS"}.`;
    },
  },
  {
    id: "duplicates-merged",
    title: "Two calls, one emergency",
    description: "Callers A and B report the same red injury at Test Clinic 1. The second call merges into the first, which keeps dispatching.",
    needsReset: true,
    steps: [
      { kind: "dial", label: "Caller A reports a red injury at Test Clinic 1", phone: "caller-a", inputs: path("1", "1", "1", "2"), as: "first" },
      { kind: "dial", label: "Caller B reports the same emergency", phone: "caller-b", inputs: path("1", "1", "1", "2"), as: "second" },
      { kind: "wait", label: "Waiting for the second call to merge into the first", timeoutMs: 30000,
        until: (b, v) => {
          const a = byRef(b, v.first);
          const m = byRef(b, v.second);
          return v.first !== v.second && a && m && m.state === "merged" && m.mergedInto === a.id && a.state !== "closed" &&
            a.events.some((e) => e.type === "incident.merge_absorbed");
        } },
    ],
    result: (b, v) => `Incident ${v.second} merged into ${v.first}: one dispatch, and both calls kept on the board.`,
  },
];

// phonesOf names every phone a scenario drives.
export function phonesOf(steps) {
  const out = new Set();
  for (const s of steps) {
    if (s.phone) out.add(s.phone);
    for (const b of s.branches || []) for (const p of phonesOf(b)) out.add(p);
  }
  return out;
}

// lockPhones adds a lock on phones to locks, the set of locks held now, and
// returns its release. A phone stays locked while any lock holds it.
export function lockPhones(locks, phones, why) {
  const lock = { why, phones: new Set(phones) };
  locks.add(lock);
  return () => locks.delete(lock);
}

// lockedBy is the reason of the latest lock holding phone, or undefined.
export const lockedBy = (locks, phone) => [...locks].findLast((l) => l.phones.has(phone))?.why;

// next is the runner's state machine: idle → running(step) → done | failed
// | cancelled. An event that does not apply leaves the status as it was.
export function next(status, ev) {
  if (status.phase === "running") {
    if (ev.type === "step") return { ...status, step: ev.step, label: ev.label };
    if (ev.type === "done") return { phase: "done", id: status.id, result: ev.result };
    if (ev.type === "fail") return { phase: "failed", id: status.id, step: status.step, reason: ev.reason };
    if (ev.type === "cancel") return { phase: "cancelled", id: status.id, step: status.step };
    return status;
  }
  if (ev.type === "start") return { phase: "running", id: ev.id, step: 0, total: ev.total, label: ev.label };
  return status;
}

export const IDLE = { phase: "idle" };

async function ussd(deps, phone, body, keys) {
  const { status, data } = await deps.ussd(phone, body, keys);
  deps.signal.throwIfAborted();
  if (status !== 200) throw new Error(data.error || `The phone got status ${status}.`);
  return data;
}

// answerAlert dials the responder menu from phone and accepts or declines
// the alert it offers: the one for ref, or, without ref, whichever it shows.
// A decline that asks why takes the last reason, Other. It returns the
// alert's reference and the closing screen.
export async function answerAlert(deps, phone, ref, answer) {
  let sc = await ussd(deps, phone, { phone, code: deps.codes.responder }, deps.codes.responder);
  const shown = (REF.exec(sc.text) || [""])[0];
  if (sc.end || !shown || !sc.text.includes("1 Accept") || (ref && shown !== ref)) {
    throw new Error(`The menu on ${phone} did not offer the alert for ${ref || "an incident"}: "${sc.text}"`);
  }
  const key = answer === "accept" ? "1" : "2";
  sc = await ussd(deps, phone, { session: sc.session, input: key }, key);
  if (!sc.end) {
    const reasons = sc.text.split("\n").map((l) => /^(\d+) /.exec(l)).filter(Boolean);
    if (!reasons.length) throw new Error(`Unexpected screen after ${answer}: "${sc.text}"`);
    const last = reasons[reasons.length - 1][1];
    sc = await ussd(deps, phone, { session: sc.session, input: last }, last);
  }
  return { ref: shown, text: sc.text };
}

// menuOn dials the "My incident" menu (*384*3#) from phone for ref, and
// presses the option whose words match, then any keys after it.
async function menuOn(deps, phone, ref, words, after = []) {
  let sc = await ussd(deps, phone, { phone, code: deps.codes.incident }, deps.codes.incident);
  const line = sc.text.split("\n").find((l) => l.replace(/^\d+ /, "") === words);
  if (sc.end || !sc.text.includes(ref) || !line) throw new Error(`The menu on ${phone} did not offer "${words}" for ${ref}: "${sc.text}"`);
  for (const key of [line.split(" ")[0], ...after]) {
    if (sc.end) throw new Error(`The menu ended before the answer ${key}: "${sc.text}"`);
    sc = await ussd(deps, phone, { session: sc.session, input: key }, key);
  }
  if (!sc.end) throw new Error(`The menu did not end: "${sc.text}"`);
  return sc.text;
}

const STEP_WORDS = { arrived: "I have arrived", departed: "Patient picked up, leaving now", at_facility: "Arrived at the facility" };

const RUN = {
  async dial(s, deps, vars) {
    let sc = await ussd(deps, s.phone, { phone: s.phone, code: deps.codes.caller }, deps.codes.caller);
    for (const key of s.inputs) {
      if (sc.end) throw new Error(`The call ended before the answer ${key}: "${sc.text}"`);
      sc = await ussd(deps, s.phone, { session: sc.session, input: key }, key);
    }
    const ref = (REF.exec(sc.text) || [""])[0];
    if (!sc.end || !sc.text.startsWith("HELP IS COMING") || !ref) throw new Error(`The call did not end with help coming: "${sc.text}"`);
    vars[s.as] = ref;
  },
  async respond(s, deps, vars) {
    const ref = s.ref ? vars[s.ref] : "";
    const { ref: shown, text } = await answerAlert(deps, s.phone, ref, s.answer);
    const want = s.answer === "accept" ? "Accepted " : "Declined ";
    if (!text.includes(want + shown)) throw new Error(`The responder heard "${text}", not ${want.trim().toLowerCase()}.`);
    if (s.as) vars[s.as] = shown;
  },
  async wait(s, deps, vars) {
    const deadline = deps.now() + s.timeoutMs;
    for (;;) {
      const b = await deps.board();
      deps.signal.throwIfAborted();
      if (b && s.until(b, vars)) return;
      if (deps.now() >= deadline) throw new Error(`Timed out: ${s.label.replace(/^Waiting for/, "no sign of")}.`);
      await deps.sleep(1000);
    }
  },
  // share: a VHT's smartphone sends by itself once they accept (app.js), so
  // the step waits for it; a basic phone answers the menu.
  async share(s, deps, vars) {
    const ref = vars[s.ref];
    if (deps.smart?.("vht")) {
      await RUN.wait({ label: `Waiting for ${s.vht}'s smartphone to send`, timeoutMs: 20000, until: (b) => shareOf(b, byRef(b, ref), s.vht) }, deps, vars);
      return;
    }
    let text = "";
    for (const words of ["Send the transporter where I am", "Update where I am"]) {
      try {
        text = await menuOn(deps, s.phone, ref, words, s.keys.slice(1));
        break;
      } catch (err) {
        if (words === "Update where I am") throw err;
      }
    }
    if (!/^PENDING-RESPONDER (Sent to|Saved)/.test(text)) throw new Error(`The VHT heard "${text}", not that it was sent.`);
  },
  // step: a smartphone records it through its app; a basic phone on the menu.
  async step(s, deps, vars) {
    const ref = vars[s.ref];
    const b = await deps.board();
    const inc = b && byRef(b, ref);
    if (!inc) throw new Error(`Incident ${ref} is not on the board.`);
    const facility = s.facilityOf ? shareOf(b, inc, s.facilityOf)?.facilityId : "";
    if (s.facilityOf && !facility) throw new Error(`${s.facilityOf} has not shared a facility.`);
    if (deps.smart?.(s.role)) {
      const body = { phone: s.phone, incident: inc.id, step: s.step };
      if (facility) body.facility = facility;
      const { status, data } = await deps.progress(body);
      deps.signal.throwIfAborted();
      if (status !== 200) throw new Error(data.error || `The step got status ${status}.`);
      return;
    }
    const after = [];
    if (facility) {
      // The destination screen lists facilities by distance: find its key.
      const sc = await ussd(deps, s.phone, { phone: s.phone, code: deps.codes.incident }, deps.codes.incident);
      const key = sc.text.split("\n").find((l) => l.replace(/^\d+ /, "") === STEP_WORDS[s.step])?.split(" ")[0];
      if (!key) throw new Error(`The menu did not offer "${STEP_WORDS[s.step]}": "${sc.text}"`);
      const list = await ussd(deps, s.phone, { session: sc.session, input: key }, key);
      const name = shareOf(b, inc, s.facilityOf).facility;
      const pick = list.text.split("\n").find((l) => l.replace(/^\d+ /, "") === name)?.split(" ")[0];
      if (!pick) throw new Error(`The facility list did not offer ${name}: "${list.text}"`);
      const done = await ussd(deps, s.phone, { session: list.session, input: pick }, pick);
      if (!done.end || !done.text.includes("Handed over")) throw new Error(`The rider heard "${done.text}".`);
      return;
    }
    const text = await menuOn(deps, s.phone, ref, STEP_WORDS[s.step], after);
    if (/could not|before|already|has not|no open/.test(text)) throw new Error(`The menu answered "${text}".`);
  },
  // pace sets how often callers are asked whether help has arrived.
  async pace(s, deps) {
    const { status, data } = await deps.pace(s.seconds);
    if (status !== 200) throw new Error(data.error || `The pace got status ${status}.`);
  },
  // answer waits for the arrival question on the caller's phone, then
  // answers it: on the USSD push a basic phone shows, or in the app.
  async answer(s, deps, vars) {
    const ref = vars[s.ref];
    let check = null;
    await RUN.wait({ label: `Waiting for the question to ${s.phone}`, timeoutMs: s.timeoutMs || 60000,
      until: (b) => { check = checkOf(b, ref); return check?.open; } }, deps, vars);
    if (deps.smart?.("caller")) {
      const { status, data } = await deps.arrival({ phone: s.phone, incident: check.incident, answer: s.yes ? "yes" : "no" });
      deps.signal.throwIfAborted();
      if (status !== 200) throw new Error(data.error || `The answer got status ${status}.`);
      return;
    }
    const key = s.yes ? "1" : "2";
    const sc = await ussd(deps, s.phone, { session: check.open.session, input: key }, key);
    if (!sc.end || !/Thank you|Sorry/.test(sc.text)) throw new Error(`The caller heard "${sc.text}".`);
  },
  pause: (s, deps) => deps.sleep(s.ms),
  reset: (s, deps) => deps.reset(),
  parallel: (s, deps, vars) => Promise.all(s.branches.map((steps) => runSteps(steps, deps, vars))),
};

async function runSteps(steps, deps, vars) {
  for (const s of steps) await RUN[s.kind](s, deps, vars);
}

// runScenario runs one scenario to its end and returns the final status.
// deps: codes {caller, responder}, ussd(phone, body, keys) → {status, data},
// board() → board, reset(), sleep(ms) and now(), and an AbortSignal; sleep
// and ussd reject once it is aborted. A failed parallel branch leaves its
// siblings running: the caller aborts the signal once this returns.
export async function runScenario(sc, deps, onStatus = () => {}) {
  const steps = (sc.needsReset ? [{ kind: "reset", label: "Resetting the demo" }] : []).concat(sc.steps);
  let status = next(IDLE, { type: "start", id: sc.id, total: steps.length, label: steps[0].label });
  onStatus(status);
  const vars = {};
  try {
    for (const [i, s] of steps.entries()) {
      status = next(status, { type: "step", step: i, label: s.label });
      onStatus(status);
      await RUN[s.kind](s, deps, vars);
      deps.signal.throwIfAborted();
    }
    const b = await deps.board();
    deps.signal.throwIfAborted();
    status = next(status, { type: "done", result: sc.result(b, vars) });
  } catch (err) {
    status = next(status, deps.signal.aborted ? { type: "cancel" } : { type: "fail", reason: err.message });
  }
  onStatus(status);
  return status;
}
