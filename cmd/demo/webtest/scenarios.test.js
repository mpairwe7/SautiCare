import { test } from "node:test";
import assert from "node:assert/strict";
import { SCENARIOS, runScenario, next, IDLE, phonesOf, answerAlert, lockPhones, lockedBy } from "../web/scenarios.js";

const codes = { caller: "*384*1#", responder: "*384*2#" };
const labelOf = (id) => id.replace(/^(\w)(\w*)-(\d+)$/, (_, a, b, n) => (a === "v" ? "VHT" : a.toUpperCase() + b) + " " + n);

// fakeDemo answers the USSD API as intake-gw does, far enough for the
// scenarios: Tool 1's eight answers end with an incident number; each call
// alerts the responders named in `alerts` who hold no other pending alert
// (one alert per responder, ADR-0044; the calls are all at one place, so
// none is nearer); the responder menu offers a responder's pending alert.
// Its board follows.
function fakeDemo({ alerts = [], reasons = false } = {}) {
  const calls = [];
  const sessions = new Map();
  const incidents = [];
  let n = 0;
  let clock = 0;
  const latest = (label) => incidents.flatMap((i) => i.alerts.filter((a) => a.responder === label && a.outcome === "pending").map((a) => [i, a]))[0];
  const busy = (label) => incidents.some((i) => !["closed", "merged"].includes(i.state) && i.alerts.some((a) => a.responder === label && a.outcome === "accepted"));
  const screen = (session, text, end) => ({ status: 200, data: { session: end ? "" : session, text, end } });
  const ussd = async (phone, body) => {
    calls.push([phone, body.code || body.input]);
    if (body.code) {
      const s = "s" + ++n;
      sessions.set(s, { phone, code: body.code, inputs: [] });
      if (body.code === codes.caller) return screen(s, "Sauti emergency help\n1 English", false);
      const hit = latest(labelOf(phone));
      sessions.get(s).hit = hit;
      return screen(s, hit ? `PENDING-RESPONDER Sauti ${hit[0].reference}: RED\n1 Accept\n2 Decline\n3 Availability` : "Sauti: no active alert.\n3 Availability", false);
    }
    const s = sessions.get(body.session);
    s.inputs.push(body.input);
    if (s.code === codes.caller) {
      if (s.inputs.length < 8) return screen(body.session, "PENDING-TOOL1 screen " + (s.inputs.length + 1), false);
      const ref = `853 402 446${incidents.length}`;
      incidents.unshift({ id: "i" + incidents.length, reference: ref, severity: "red", state: "dispatching", t: { T1: new Date(0).toISOString() },
        escalations: [], events: [], alerts: alerts.filter((r) => !latest(r[0]) && !busy(r[0])).map((r, k) => ({ id: ref + k, responder: r[0], role: r[1], wave: 1, outcome: "pending" })) });
      return screen(body.session, `HELP IS COMING\nIncident ${ref}\nPENDING-TOOL1 first aid line`, true);
    }
    const [inc, a] = s.hit;
    if (body.input === "1") {
      a.outcome = "accepted";
      if (["vht", "transporter"].every((role) => inc.alerts.some((x) => x.role === role && x.outcome === "accepted"))) {
        inc.state = "assigned";
        inc.t.T4 = new Date(5000).toISOString();
      }
      return screen(body.session, `PENDING-RESPONDER Accepted ${inc.reference}. Go to Test Clinic 1.`, true);
    }
    if (reasons && s.inputs.length === 1) return screen(body.session, "Why can you not go?\n1 Too far\n2 Unwell\n3 Other", false);
    a.outcome = "declined";
    a.reason = s.inputs[1] || "";
    return screen(body.session, `PENDING-RESPONDER Declined ${inc.reference}. Thank you.`, true);
  };
  const ctl = new AbortController();
  const deps = {
    codes, ussd, signal: ctl.signal, now: () => clock,
    board: async () => ({ incidents: structuredClone(incidents) }),
    sleep: async (ms) => { ctl.signal.throwIfAborted(); clock += ms; },
    reset: async () => { calls.push(["reset"]); incidents.length = 0; },
  };
  return { deps, ctl, calls, incidents };
}

const red = SCENARIOS.find((s) => s.id === "red-accepted");

test("the runner's status moves idle → running(step) → done, failed or cancelled, and nowhere after", () => {
  let s = next(IDLE, { type: "start", id: "x", total: 3, label: "one" });
  assert.deepEqual(s, { phase: "running", id: "x", step: 0, total: 3, label: "one" });
  s = next(s, { type: "step", step: 2, label: "three" });
  assert.equal(s.step, 2);
  const cancelled = next(s, { type: "cancel" });
  assert.deepEqual(cancelled, { phase: "cancelled", id: "x", step: 2 });
  assert.equal(next(cancelled, { type: "done", result: "late" }), cancelled, "a run that ended stays ended");
  assert.equal(next(IDLE, { type: "step", step: 1 }), IDLE);
  assert.equal(next(s, { type: "fail", reason: "r" }).phase, "failed");
});

test("a scenario resets, walks Tool 1 and the responder menu key by key, and reports the board's result", async () => {
  const demo = fakeDemo({ alerts: [["VHT 1", "vht"], ["VHT 2", "vht"], ["Transporter 1", "transporter"]] });
  const seen = [];
  const end = await runScenario(red, demo.deps, (s) => seen.push(s.phase === "running" ? s.step : s.phase));
  assert.equal(end.phase, "done", end.reason);
  assert.match(end.result, /^Incident 853 402 4460: VHT 1 and Transporter 1 accepted, 0 other alert\(s\) stood down, call to acceptance 0:05\.$/);
  assert.deepEqual(seen, [0, 0, 1, 2, 3, 4, 5, "done"], "one status per step, from the reset");
  assert.deepEqual(demo.calls, [["reset"], ["caller-a", "*384*1#"], ...["1", "1", "1", "3", "2", "1", "2", "1"].map((k) => ["caller-a", k]),
    ["vht-1", "*384*2#"], ["vht-1", "1"], ["transporter-1", "*384*2#"], ["transporter-1", "1"]]);
});

test("a wait that never sees its condition fails the run with what it waited for", async () => {
  const demo = fakeDemo({ alerts: [["VHT 1", "vht"]] }); // VHT 2 and Transporter 1 are never alerted
  const end = await runScenario(red, demo.deps);
  assert.deepEqual(end, { phase: "failed", id: "red-accepted", step: 2, reason: "Timed out: no sign of the alerts to VHT 1, VHT 2 and Transporter 1." });
  assert.ok(!demo.calls.some(([p]) => p === "vht-1"), "no responder answers after the failure");
});

test("cancelling stops the run before its next keypress", async () => {
  const demo = fakeDemo();
  const ussd = demo.deps.ussd;
  demo.deps.ussd = async (phone, body) => {
    const r = await ussd(phone, body);
    if (body.input === "3") demo.ctl.abort(); // the user presses Cancel on screen 4
    return r;
  };
  const end = await runScenario(red, demo.deps);
  assert.equal(end.phase, "cancelled");
  assert.deepEqual(demo.calls.at(-1), ["caller-a", "3"], "no key is sent after the cancel");
});

test("a cancel while the final board is read ends the run cancelled, not done", async () => {
  const demo = fakeDemo();
  const sc = { id: "one", title: "One", steps: [{ kind: "pause", label: "A pause", ms: 1 }], result: () => "finished" };
  demo.deps.board = async () => { demo.ctl.abort(); return { incidents: [] }; };
  assert.deepEqual(await runScenario(sc, demo.deps), { phase: "cancelled", id: "one", step: 0 });
});

test("a decline that asks why answers with the last reason", async () => {
  const demo = fakeDemo({ alerts: [["VHT 4", "vht"]], reasons: true });
  await demo.deps.ussd("caller-b", { code: codes.caller });
  for (let k = 0; k < 8; k++) await demo.deps.ussd("caller-b", { session: "s1", input: "1" });
  const out = await answerAlert(demo.deps, "vht-4", "853 402 4460", "decline");
  assert.equal(out.text, "PENDING-RESPONDER Declined 853 402 4460. Thank you.");
  assert.equal(demo.incidents[0].alerts[0].reason, "3");
  await assert.rejects(answerAlert(demo.deps, "vht-4", "853 402 4460", "accept"), /did not offer the alert/, "an alert is answered once");
});

test("a responder holds one alert: a second call does not alert them while the first waits", async () => {
  const demo = fakeDemo({ alerts: [["VHT 4", "vht"]] });
  for (const phone of ["caller-a", "caller-b"]) {
    const { data } = await demo.deps.ussd(phone, { code: codes.caller });
    for (let k = 0; k < 8; k++) await demo.deps.ussd(phone, { session: data.session, input: "1" });
  }
  assert.equal(demo.incidents[0].alerts.length, 0, "the second call waits for a free responder");
  assert.equal((await answerAlert(demo.deps, "vht-4", "", "accept")).ref, "853 402 4460", "the first call's alert, the only one");
});

test("the surge's branches run together, and every phone they drive is listed", async () => {
  const surge = SCENARIOS.find((s) => s.id === "surge");
  assert.deepEqual([...phonesOf(surge.steps)].sort(), ["caller-a", "caller-b", "vht-4"]);
  const demo = fakeDemo({ alerts: [["VHT 4", "vht"]] });
  const end = await runScenario(surge, demo.deps);
  assert.equal(end.phase, "done", end.reason);
  assert.match(end.result, /^5 calls, 5 incidents, none dropped: VHT 4 took 853 402 4460; 4 waiting for a free responder, 0 with an alert out, 0 escalated; no responder held two alerts\.$/);
  const vht = demo.calls.findIndex(([p]) => p === "vht-4");
  const lastCall = demo.calls.findLastIndex(([p]) => p.startsWith("caller"));
  assert.ok(vht < lastCall, "VHT 4 answers while calls are still coming in");
});

test("a phone stays locked while any lock holds it, and shows the latest lock's reason", () => {
  for (const scenarioEndsFirst of [false, true]) {
    const locks = new Set();
    const autopilot = lockPhones(locks, ["vht-4"], "The autopilot is answering.");
    const scenario = lockPhones(locks, new Set(["caller-a", "vht-4"]), "A scenario is dialling.");
    assert.equal(lockedBy(locks, "vht-4"), "A scenario is dialling.");
    const [first, second] = scenarioEndsFirst ? [scenario, autopilot] : [autopilot, scenario];
    first();
    assert.equal(lockedBy(locks, "vht-4"), scenarioEndsFirst ? "The autopilot is answering." : "A scenario is dialling.", `vht-4 after the first release (scenario first: ${scenarioEndsFirst})`);
    assert.equal(lockedBy(locks, "caller-a"), scenarioEndsFirst ? undefined : "A scenario is dialling.", `caller-a after the first release (scenario first: ${scenarioEndsFirst})`);
    second();
    assert.equal(lockedBy(locks, "vht-4"), undefined);
    assert.equal(lockedBy(locks, "caller-a"), undefined);
  }
});

test("every scenario is a table of known step kinds with a title, a description and a result", () => {
  const kinds = new Set(["dial", "respond", "wait", "pause", "parallel", "share", "step", "pace", "answer"]);
  const walk = (steps) => steps.every((s) => kinds.has(s.kind) && s.label && (!s.branches || s.branches.every(walk)));
  for (const s of SCENARIOS) assert.ok(s.title && s.description && typeof s.result === "function" && walk(s.steps), s.id);
  assert.equal(new Set(SCENARIOS.map((s) => s.id)).size, SCENARIOS.length);
});
