import { test } from "node:test";
import assert from "node:assert/strict";
import { metrics, timeline, cascade, ticks, boardSms } from "../web/metrics.js";

const at = (s) => new Date(Date.UTC(2026, 10, 16, 9, 0, s)).toISOString();

const alert = (id, responder, role, outcome, wave, sent, responded) =>
  ({ id, responder, role, ring: 1, wave, sentAt: at(sent), outcome, respondedAt: responded === undefined ? undefined : at(responded), attempts: [] });

const board = {
  incidents: [
    { id: "a", reference: "111 111 1111", severity: "red", state: "assigned", t: { T1: at(0), T2: at(2), T3: at(3), T4: at(8) },
      escalations: [], events: [],
      alerts: [alert("x1", "VHT 1", "vht", "accepted", 1, 3, 8), alert("x2", "VHT 2", "vht", "stood_down", 1, 3)] },
    { id: "b", reference: "222 222 2222", severity: "green", state: "dispatching", t: { T1: at(10), T2: at(12) },
      escalations: [{ at: at(13), reason: "no_eligible_responder", role: "vht" }, { at: at(13), reason: "no_eligible_responder", role: "transporter" }],
      events: [], alerts: [] },
    { id: "c", reference: "333 333 3333", severity: "red", state: "merged", t: { T1: at(20), T2: at(21) }, escalations: [], events: [],
      alerts: [alert("x3", "VHT 3", "vht", "pending", 1, 22)] },
  ],
};

test("the strip counts open incidents by severity, alerts by outcome, escalations and SMS", () => {
  const m = metrics(board, { "vht-1": [{}, {}], "caller-a": [{}], "vht-2": [] });
  assert.deepEqual(m.active, { red: 1, yellow: 0, green: 1, none: 0 }, "the merged incident is not open");
  assert.equal(m.activeTotal, 2);
  assert.deepEqual(m.outcomes, { pending: 1, accepted: 1, declined: 0, no_response: 0, stood_down: 1 });
  assert.equal(m.alerts, 3);
  assert.deepEqual(m.escalations, { count: 2, incidents: 1 });
  assert.deepEqual(m.sms, { count: 3, phones: 2 });
});

test("the SMS in the figures are those about the board's incidents, and those about none", () => {
  const sms = {
    "vht-1": [{ at: at(1), text: "PENDING-SMS Sauti 111 111 1111: RED Injury." }, { at: at(2), text: "PENDING-SMS Sauti 999 999 9999: RED Injury." }],
    "vht-2": [{ at: at(3), text: "PENDING-SMS Sauti: you are available until 10:00." }],
    "caller-a": [{ at: at(4), text: "PENDING-SMS Sauti 999 999 9999: help is on the way." }],
  };
  const kept = boardSms(board, sms);
  assert.deepEqual(kept, { "vht-1": [sms["vht-1"][0]], "vht-2": sms["vht-2"], "caller-a": [] }, "999 999 9999 is off the board");
  assert.deepEqual(metrics(board, kept).sms, { count: 2, phones: 2 });
});

test("call to acceptance is the median T1 to T4 over accepted incidents", () => {
  assert.deepEqual(metrics(board, {}).acceptance, { median: 8000, count: 1 });
  const two = { incidents: [...board.incidents, { ...board.incidents[0], id: "d", t: { T1: at(30), T4: at(34) } }] };
  assert.deepEqual(metrics(two, {}).acceptance, { median: 6000, count: 2 });
  assert.deepEqual(metrics({ incidents: [] }, {}).acceptance, { median: null, count: 0 });
});

test("the timeline lays the reached intervals from T1 and runs the current one to now", () => {
  const open = { state: "dispatching", t: { T1: at(0), T2: at(2), T3: at(3) } };
  const tl = timeline(open, Date.parse(at(10)));
  assert.deepEqual(tl.segments.map((s) => [s.label, s.start, s.end, s.open]), [
    ["Intake", 0, 2000, false],
    ["Finding responders", 2000, 3000, false],
    ["Waiting for acceptance", 3000, 10000, true],
  ]);
  assert.equal(tl.span, 10000);
  assert.deepEqual(tl.ticks, [0, 2000, 4000, 6000, 8000, 10000]);
  assert.deepEqual(tl.milestones.map((m) => m.key), ["T1", "T2", "T3"]);
});

test("an ended incident's timeline has no open interval", () => {
  const merged = { state: "merged", t: { T1: at(0), T2: at(5) } };
  assert.deepEqual(timeline(merged, Date.parse(at(60))).segments.map((s) => s.open), [false]);
  assert.equal(timeline({ state: "intake", t: {} }, 0), null, "no T1, no timeline");
});

test("ticks stay round and at most six intervals apart", () => {
  assert.deepEqual(ticks(1000), [0, 1000]);
  assert.deepEqual(ticks(95000), [0, 30000, 60000, 90000]);
  for (const span of [1500, 9000, 47000, 400000, 3600000]) assert.ok(ticks(span).length <= 7, `span ${span}`);
  const hour = 3600000;
  for (const span of [6 * hour + 1000, 24 * hour, 72 * hour]) {
    const t = ticks(span);
    assert.ok(t.length >= 2 && t.length <= 7, `span ${span / hour} h: ${t.length} ticks`);
    assert.equal(t[1] % hour, 0, `span ${span / hour} h: a whole number of hours apart`);
  }
});

test("the cascade groups alerts by wave, with how long each took to end", () => {
  const inc = {
    events: [{ type: "alert.stood_down", at: at(9), payload: JSON.stringify({ alert: "x2" }) }],
    alerts: [alert("x1", "VHT 1", "vht", "accepted", 1, 3, 8), alert("x2", "VHT 2", "vht", "stood_down", 1, 3),
      alert("x4", "VHT 4", "vht", "pending", 2, 40)],
  };
  const waves = cascade(inc);
  assert.deepEqual(waves.map((w) => w.wave), [1, 2]);
  assert.deepEqual(waves[0].alerts.map((a) => [a.responder, a.outcome, a.took]), [["VHT 1", "accepted", 5000], ["VHT 2", "stood_down", 6000]]);
  assert.equal(waves[1].alerts[0].took, null, "a pending alert has not ended");
});
