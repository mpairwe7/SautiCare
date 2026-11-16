import { test } from "node:test";
import assert from "node:assert/strict";
import { responderStatus, actionable, nextStep, partner, callerProgress, arrivedRoles } from "../web/responders.js";

const at = (s) => new Date(Date.UTC(2026, 10, 16, 12, 0, s)).toISOString();
const inc = (over) => ({ id: "i1", reference: "284 262 7491", state: "dispatching", severity: "red", escalated: false,
  t: { T1: at(0) }, alerts: [], escalations: [], events: [], ...over });
const alert = (responder, role, outcome, s) => ({ responder, role, outcome, sentAt: at(s), respondedAt: outcome === "accepted" ? at(s + 9) : undefined });

test("a pending alert is offered with its deadline", () => {
  const b = { incidents: [inc({ alerts: [alert("VHT 1", "vht", "pending", 10)] })] };
  const s = responderStatus(b, "VHT 1", 90000);
  assert.equal(s.kind, "alert");
  assert.equal(s.late, false);
  assert.equal(s.deadline, Date.parse(at(10)) + 90000);
  assert.equal(responderStatus(b, "VHT 2", 90000).kind, "idle");
});

test("a lapsed alert stays acceptable while its role is open, as the menu offers it", () => {
  const open = inc({ alerts: [alert("VHT 1", "vht", "no_response", 10), alert("Transporter 1", "transporter", "accepted", 10)], state: "assigned" });
  const s = responderStatus({ incidents: [open] }, "VHT 1", 90000);
  assert.equal(s.kind, "alert");
  assert.equal(s.late, true);
  const taken = inc({ alerts: [alert("VHT 1", "vht", "no_response", 10), alert("VHT 2", "vht", "accepted", 10)], state: "assigned" });
  assert.equal(actionable(taken, taken.alerts[0]), false);
  assert.equal(responderStatus({ incidents: [taken] }, "VHT 1", 90000).kind, "idle");
  const closed = inc({ alerts: [alert("VHT 1", "vht", "no_response", 10)], state: "closed" });
  assert.equal(actionable(closed, closed.alerts[0]), false);
});

test("an accepted open incident comes before any other alert, and ends when it closes", () => {
  const job = inc({ id: "a", state: "assigned", alerts: [alert("VHT 1", "vht", "accepted", 5)] });
  const fresh = inc({ id: "b", reference: "111 222 3334", alerts: [alert("VHT 1", "vht", "pending", 50)] });
  assert.equal(responderStatus({ incidents: [fresh, job] }, "VHT 1", 90000).kind, "busy");
  job.state = "closed";
  const s = responderStatus({ incidents: [fresh, job] }, "VHT 1", 90000);
  assert.equal(s.kind, "alert");
  assert.equal(s.inc.id, "b");
  assert.equal(s.recent.length, 2);
});

test("the next step follows the role and the lifecycle", () => {
  const i = inc({ state: "assigned" });
  assert.equal(nextStep(i, "vht"), "arrived");
  assert.equal(nextStep(i, "transporter"), "arrived");
  i.state = "on_scene";
  i.events = [{ type: "responder.arrived", payload: '{"role":"vht"}' }];
  assert.deepEqual([...arrivedRoles(i)], ["vht"]);
  assert.equal(nextStep(i, "vht"), "");
  assert.equal(nextStep(i, "transporter"), "arrived");
  i.events.push({ type: "responder.arrived", payload: '{"role":"transporter"}' });
  assert.equal(nextStep(i, "transporter"), "departed");
  i.state = "transporting";
  i.t.T6 = at(300);
  assert.equal(nextStep(i, "transporter"), "at_facility");
  i.state = "closed";
  i.t.T7 = at(600);
  assert.equal(nextStep(i, "transporter"), "");
});

test("a VHT who did not arrive before the patient left has no step left", () => {
  const i = inc({ state: "transporting", t: { T1: at(0), T6: at(100) }, events: [{ type: "responder.arrived", payload: '{"role":"transporter"}' }] });
  assert.equal(nextStep(i, "vht"), "");
});

test("the partner line names who else is coming, or what is happening instead", () => {
  const i = inc({ state: "assigned", alerts: [alert("VHT 1", "vht", "accepted", 5), alert("Transporter 1", "transporter", "pending", 5)] });
  assert.equal(partner(i, "vht").text, "Alerting a transporter…");
  i.alerts[1].outcome = "accepted";
  assert.equal(partner(i, "vht").text, "Transporter 1 is coming too");
  assert.equal(partner(i, "transporter").text, "VHT 1 is coming too");
  const esc = inc({ escalations: [{ role: "transporter", reason: "no_eligible_responder" }] });
  assert.equal(partner(esc, "vht").tone, "declined");
  i.events = [{ type: "responder.arrived", payload: '{"role":"vht"}' }];
  assert.equal(partner(i, "transporter").text, "VHT 1 is on scene");
});

test("the caller's tracker never names a responder", () => {
  const i = inc({ state: "on_scene", t: { T1: at(0), T5: at(200) }, alerts: [alert("VHT 1", "vht", "accepted", 5)] });
  const p = callerProgress(i);
  assert.equal(p.current, "transport");
  assert.ok(p.steps.every((s) => !s.label.includes("VHT 1")));
  assert.equal(p.steps.find((s) => s.key === "arrived").done, true);
});

test("the caller hears of an escalation only while a helper is still missing", () => {
  const i = inc({ escalated: true, state: "assigned", alerts: [alert("VHT 1", "vht", "accepted", 5)] });
  assert.equal(callerProgress(i).escalated, true);
  i.alerts.push(alert("Transporter 1", "transporter", "accepted", 100));
  assert.equal(callerProgress(i).escalated, false);
});
