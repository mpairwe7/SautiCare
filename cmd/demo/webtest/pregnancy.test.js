import { test } from "node:test";
import assert from "node:assert/strict";
import { SCENARIOS, runScenario } from "../web/scenarios.js";

// The pregnancy scenario against a fake of the demo: Tool 1, the responder
// menu (*384*2#), the "My incident" menu (*384*3#), the smartphone's
// progress API and the shares on the board, far enough for the scenario.
const codes = { caller: "*384*1#", responder: "*384*2#", incident: "*384*3#" };
const labelOf = { "vht-1": "VHT 1", "transporter-1": "Transporter 1" };
const STEP = { arrived: "I have arrived", departed: "Patient picked up, leaving now", at_facility: "Arrived at the facility" };
const FACILITIES = ["Test Health Centre 1", "Test Clinic 3", "Test Health Centre 2"]; // nearest Test School 1 first
const ID = { "Test Health Centre 1": "hc_test_1", "Test Clinic 3": "hc_test_3", "Test Health Centre 2": "hc_test_2" };

function fake(smart) {
  const sessions = new Map();
  const shares = [];
  const progress = [];
  let inc = null;
  let n = 0;
  let clock = 0;
  const screen = (session, text, end) => ({ status: 200, data: { session: end ? "" : session, text, end } });
  const arrived = (role) => inc.events.some((e) => e.type === "responder.arrived" && JSON.parse(e.payload).role === role);
  const next = (role) => {
    if (inc.state === "closed") return "";
    if (!arrived(role)) return "arrived";
    if (role !== "transporter") return "";
    return !inc.t.T6 ? "departed" : !inc.t.T7 ? "at_facility" : "";
  };
  const record = (role, step, facility) => {
    if (step === "arrived") {
      inc.events.push({ type: "responder.arrived", payload: JSON.stringify({ role }) });
      inc.state = "on_scene";
      inc.t.T5 ||= new Date(clock).toISOString();
    } else if (step === "departed") {
      inc.state = "transporting";
      inc.t.T6 = new Date(clock).toISOString();
    } else {
      inc.t.T7 = new Date(clock + 60000).toISOString();
      inc.facility = facility;
      inc.state = "closed";
    }
    progress.push([role, step, facility || ""]);
  };
  const ussd = async (phone, body) => {
    if (body.code) {
      const s = "s" + ++n;
      sessions.set(s, { phone, code: body.code, inputs: [] });
      if (body.code === codes.caller) return screen(s, "Sauti emergency help\n1 English", false);
      const label = labelOf[phone];
      const a = inc?.alerts.find((x) => x.responder === label);
      if (body.code === codes.responder) {
        return screen(s, a?.outcome === "pending" ? `PENDING-RESPONDER Sauti ${inc.reference}: RED\n1 Accept\n2 Decline\n3 Availability` : "Sauti: no active alert.", false);
      }
      if (a?.outcome !== "accepted") return screen(s, "PENDING-RESPONDER Sauti: you have no open incident.", true);
      const role = a.role;
      const opts = role === "vht" ? [shares.length ? "Update where I am" : "Send the transporter where I am"] : [];
      if (next(role)) opts.push(STEP[next(role)]);
      sessions.get(s).opts = opts;
      return screen(s, [`PENDING-RESPONDER My incident ${inc.reference}`, "Test School 1, Test Ward 1", ...opts.map((o, i) => `${i + 1} ${o}`)].join("\n"), false);
    }
    const s = sessions.get(body.session);
    s.inputs.push(body.input);
    const label = labelOf[s.phone];
    if (s.code === codes.caller) {
      if (s.inputs.length < 8) return screen(body.session, "PENDING-TOOL1 screen " + (s.inputs.length + 1), false);
      inc = { id: "i0", reference: "853 402 4460", severity: "red", state: "dispatching", t: { T1: new Date(0).toISOString() }, events: [], escalations: [],
        alerts: [{ id: "a1", responder: "VHT 1", role: "vht", outcome: "pending" }, { id: "a2", responder: "Transporter 1", role: "transporter", outcome: "pending" }] };
      return screen(body.session, `HELP IS COMING\nIncident ${inc.reference}\nPENDING-TOOL1 first aid line`, true);
    }
    if (s.code === codes.responder) {
      inc.alerts.find((x) => x.responder === label).outcome = "accepted";
      if (inc.alerts.every((x) => x.outcome === "accepted")) inc.state = "assigned";
      return screen(body.session, `PENDING-RESPONDER Accepted ${inc.reference}. Go to Test School 1, Test Ward 1.`, true);
    }
    const role = inc.alerts.find((x) => x.responder === label).role;
    const chosen = s.opts[Number(s.inputs[0]) - 1];
    if (chosen === "Send the transporter where I am" || chosen === "Update where I am") {
      assert.ok(smart === false, "a smartphone VHT shares through its app, not the menu");
      if (s.inputs.length < 5) return screen(body.session, `PENDING-RESPONDER screen ${s.inputs.length + 1}`, false);
      assert.deepEqual(s.inputs, ["1", "1", "1", "1", "4"]);
      shares.push({ incident: inc.id, vht: "VHT 1", landmark: "Test Church 1", ward: "Test Ward 1", km: 0.3, facility: "Test Health Centre 1", facilityId: "hc_test_1", via: "ussd", told: true });
      return screen(body.session, "PENDING-RESPONDER Sent to Transporter 1: you are near Test Church 1, Test Ward 1.", true);
    }
    const step = Object.keys(STEP).find((k) => STEP[k] === chosen);
    if (step === "at_facility") {
      if (s.inputs.length === 1) return screen(body.session, ["PENDING-RESPONDER Which facility?", ...FACILITIES.map((f, i) => `${i + 1} ${f}`)].join("\n"), false);
      const f = FACILITIES[Number(s.inputs[1]) - 1];
      record(role, step, ID[f]);
      return screen(body.session, `PENDING-RESPONDER Handed over at ${f}. Job complete, thank you.`, true);
    }
    record(role, step);
    return screen(body.session, step === "arrived" ? `PENDING-RESPONDER Arrival recorded for ${inc.reference}.` : `PENDING-RESPONDER ${inc.reference}: on the way to the facility.`, true);
  };
  const ctl = new AbortController();
  const deps = {
    codes, ussd, signal: ctl.signal, now: () => clock,
    smart: () => smart,
    progress: async (body) => {
      const a = inc.alerts.find((x) => x.responder === labelOf[body.phone]);
      if (a?.outcome !== "accepted" || next(a.role) !== body.step) return { status: 409, data: { error: "refused" } };
      record(a.role, body.step, body.facility);
      return { status: 200, data: { state: inc.state } };
    },
    board: async () => {
      // A VHT's smartphone sends by itself once they accept (app.js).
      if (smart && inc && inc.alerts[0].outcome === "accepted" && !shares.length) {
        shares.push({ incident: inc.id, vht: "VHT 1", landmark: "Test Clinic 1", ward: "Test Ward 1", km: 0.2, facility: "Test Health Centre 1", facilityId: "hc_test_1", via: "app", told: true });
      }
      return { incidents: inc ? [structuredClone(inc)] : [], shares: structuredClone(shares) };
    },
    sleep: async (ms) => { ctl.signal.throwIfAborted(); clock += ms; },
    reset: async () => { inc = null; shares.length = 0; },
  };
  return { deps, progress, inc: () => inc };
}

const pregnancy = SCENARIOS.find((s) => s.id === "pregnancy");

test("the pregnancy scenario runs on basic phones: the VHT sends their place on *384*3#, both arrive and the rider hands over at the VHT's facility", async () => {
  const demo = fake(false);
  const end = await runScenario(pregnancy, demo.deps);
  assert.equal(end.phase, "done", end.reason);
  assert.deepEqual(demo.progress, [["vht", "arrived", ""], ["transporter", "arrived", ""], ["transporter", "departed", ""], ["transporter", "at_facility", "hc_test_1"]]);
  assert.match(end.result, /^Incident 853 402 4460: Transporter 1 was told VHT 1 is near Test Church 1, 0\.3 km from the patient, works at Test Health Centre 1, with VHT 1's number \(from \*384\*3#\); handed over at Test Health Centre 1 \d+:\d\d after the call\.$/);
});

test("the pregnancy scenario runs on smartphones: the VHT's app sends their place by itself, and each step goes through the app", async () => {
  const demo = fake(true);
  const end = await runScenario(pregnancy, demo.deps);
  assert.equal(end.phase, "done", end.reason);
  assert.equal(demo.progress.at(-1)[2], "hc_test_1", "the rider takes the mother to the VHT's facility");
  assert.match(end.result, /told VHT 1 is near Test Clinic 1, 0\.2 km from the patient, works at Test Health Centre 1, with VHT 1's number \(from VHT 1's smartphone\)/);
});

test("the pregnancy scenario stops, saying why, when the VHT's place never reaches the rider", async () => {
  const demo = fake(true);
  const board = demo.deps.board;
  demo.deps.board = async () => ({ ...(await board()), shares: [] });
  const end = await runScenario(pregnancy, demo.deps);
  assert.equal(end.phase, "failed");
  assert.match(end.reason, /Timed out: no sign of VHT 1's smartphone to send\./);
});
