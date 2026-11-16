import { test } from "node:test";
import assert from "node:assert/strict";
import { groupOf, order, counts, filtered, overdue, roleCell, OVERDUE_MS } from "../web/queue.js";
import { distanceM, nearest, trip } from "../web/geo.js";

const at = (s) => new Date(Date.UTC(2026, 10, 16, 12, 0, s)).toISOString();
const inc = (id, over) => ({ id, reference: id, state: "dispatching", severity: "red", escalated: false, t: { T1: at(0) },
  alerts: [], escalations: [], events: [], ...over });

test("incidents group as a dispatcher works them", () => {
  assert.equal(groupOf(inc("a")), "waiting");
  assert.equal(groupOf(inc("b", { state: "assigned" })), "assigned");
  assert.equal(groupOf(inc("c", { state: "assigned", escalated: true })), "escalated");
  assert.equal(groupOf(inc("d", { state: "at_facility" })), "done");
  assert.equal(groupOf(inc("e", { state: "merged", escalated: true })), "done");
});

test("the most urgent come first: severity, then the longest wait; done last", () => {
  const list = [
    inc("green-old", { severity: "green", t: { T1: at(0) } }),
    inc("red-new", { severity: "red", t: { T1: at(50) } }),
    inc("red-old", { severity: "red", t: { T1: at(10) } }),
    inc("done", { severity: "red", state: "closed", t: { T1: at(0), T7: at(400) } }),
    inc("none", { severity: "", t: { T1: at(5) } }),
  ];
  assert.deepEqual(order(list).map((i) => i.id), ["red-old", "red-new", "green-old", "none", "done"]);
  const c = counts(list);
  assert.equal(c.all, 5);
  assert.equal(c.waiting, 4);
  assert.equal(c.done, 1);
  assert.deepEqual(filtered(list, "done").map((i) => i.id), ["done"]);
});

test("a call is overdue once it waits past the threshold with nobody accepted", () => {
  const i = inc("a");
  const t1 = Date.parse(at(0));
  assert.equal(overdue(i, t1 + OVERDUE_MS - 1000), false);
  assert.equal(overdue(i, t1 + OVERDUE_MS + 1000), true);
  i.t.T4 = at(30);
  assert.equal(overdue(i, t1 + OVERDUE_MS + 1000), false);
});

test("each role's cell says who accepted, who is alerted, or that it escalated", () => {
  const i = inc("a", { alerts: [
    { responder: "VHT 1", role: "vht", outcome: "accepted", sentAt: at(10), respondedAt: at(19) },
    { responder: "VHT 2", role: "vht", outcome: "stood_down", sentAt: at(10) },
    { responder: "Transporter 1", role: "transporter", outcome: "pending", sentAt: at(10) },
  ] });
  assert.deepEqual(roleCell(i, "vht"), { tone: "accepted", who: "VHT 1", text: "accepted", ms: 9000 });
  assert.equal(roleCell(i, "transporter").who, "Transporter 1");
  const esc = inc("b", { escalations: [{ role: "transporter", reason: "no_eligible_responder" }] });
  assert.equal(roleCell(esc, "transporter").text, "escalated");
  assert.equal(roleCell(esc, "vht").text, "waiting for a free responder");
  assert.equal(roleCell(inc("c", { state: "intake" }), "vht").text, "not alerted yet");
});

test("distances, the nearest place and trip estimates", () => {
  const a = { lat: 0.3810, lon: 32.5570 };
  const b = { lat: 0.3785, lon: 32.5548 };
  const d = distanceM(a, b);
  assert.ok(d > 350 && d < 400, `about 370 m, got ${d}`);
  assert.equal(distanceM(a, a), 0);
  assert.equal(nearest(b, [{ id: "far", lat: 0.40, lon: 32.60 }, { id: "near", lat: 0.3786, lon: 32.5549 }]).id, "near");
  assert.equal(nearest(b, []), null);
  assert.deepEqual(trip(a, b, "vht"), { km: 0.4, min: 4 }); // about 370 m at 5 km/h
  assert.equal(trip(a, b, "transporter").min, 1);
});
