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
const list = (xs) => (xs.length < 2 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);

export const SCENARIOS = [
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
    description: "Five red calls from Test Ward 2 in quick succession, while VHT 4 answers its latest alert. Every call is dispatched; none is dropped.",
    needsReset: true,
    steps: [
      { kind: "parallel", label: "Five calls arrive while VHT 4 answers", branches: [
        ["1", "2", "3", "4", "5"].map((cat, i) => ({ kind: "dial", label: `Call ${i + 1} of 5`, phone: i % 2 ? "caller-b" : "caller-a",
          inputs: path("1", cat, "2", "1"), as: "ref" + (i + 1) })),
        [
          { kind: "wait", label: "Waiting for VHT 4's first alert", timeoutMs: 30000, until: (b, v) => v.ref1 && byRef(b, v.ref1) && pendingFor(byRef(b, v.ref1), ["VHT 4"]) },
          { kind: "respond", label: "VHT 4 accepts its latest alert", phone: "vht-4", answer: "accept", as: "taken" },
        ],
      ] },
      { kind: "wait", label: "Waiting for every call to be dispatched", timeoutMs: 60000,
        until: (b, v) => [1, 2, 3, 4, 5].every((n) => {
          const i = byRef(b, v["ref" + n]);
          return i && i.severity === "red" && !i.mergedInto && i.alerts.length + i.escalations.length > 0;
        }) },
    ],
    result: (b, v) => {
      const refs = [1, 2, 3, 4, 5].map((n) => v["ref" + n]);
      const escalated = refs.filter((r) => byRef(b, r).escalated).length;
      return `${new Set(refs).size} calls, ${new Set(refs).size} incidents, none dropped: VHT 4 took ${v.taken}; ${escalated} escalated to a coordinator.`;
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
    status = next(status, { type: "done", result: sc.result(await deps.board(), vars) });
  } catch (err) {
    status = next(status, deps.signal.aborted ? { type: "cancel" } : { type: "fail", reason: err.message });
  }
  onStatus(status);
  return status;
}
