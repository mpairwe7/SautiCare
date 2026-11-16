import { test } from "node:test";
import assert from "node:assert/strict";
import { plan, choose, alertKey, AUTOPILOT } from "../web/autopilot.js";

const phones = [
  { id: "vht-1", label: "VHT 1", kind: "responder" },
  { id: "vht-2", label: "VHT 2", kind: "responder" },
  { id: "caller-a", label: "Caller A", kind: "caller" },
];
const alert = (responder, outcome, sentAt) => ({ responder, outcome, sentAt: new Date(sentAt).toISOString() });
const board = {
  incidents: [
    { reference: "111 111 1111", state: "dispatching", alerts: [alert("VHT 1", "pending", 10000), alert("VHT 2", "accepted", 10000)] },
    { reference: "222 222 2222", state: "dispatching", alerts: [alert("VHT 2", "pending", 1000), alert("VHT 9", "pending", 20000)] },
    { reference: "333 333 3333", state: "merged", alerts: [alert("VHT 1", "pending", 30000)] },
  ],
};

test("the shares decide accept, decline or ignore in proportion", () => {
  const s = { accept: 50, decline: 30, ignore: 20 };
  assert.deepEqual([0, 0.49, 0.5, 0.79, 0.8, 0.99].map((r) => choose(s, r)), ["accept", "accept", "decline", "decline", "ignore", "ignore"]);
  assert.equal(choose({ accept: 0, decline: 0, ignore: 0 }, 0.3), "ignore");
  assert.equal(choose({ accept: 0, decline: 1, ignore: 0 }, 0.99), "decline");
});

test("the autopilot plans pending alerts sent since it was switched on, and never one twice", () => {
  const decided = new Set();
  const first = plan(board, phones, { ...AUTOPILOT, delayMs: 10000 }, decided, 5000, () => 0.5);
  assert.deepEqual(first.map((d) => [d.key, d.phone, d.action, d.at]), [[alertKey("111 111 1111", "VHT 1"), "vht-1", "accept", 20000]],
    "not the accepted alert, the one sent before 5 s, the responder with no phone, or the merged incident's");
  for (const d of first) decided.add(d.key);
  assert.deepEqual(plan(board, phones, AUTOPILOT, decided, 5000), [], "a re-poll of the same board answers nothing again");
});

test("the delay is jittered within ±40% of the setting", () => {
  const ats = [0, 0.999].map((r) => plan(board, phones, { ...AUTOPILOT, delayMs: 10000 }, new Set(), 5000, () => r)[0].at);
  assert.deepEqual(ats.map(Math.round), [16000, 23992]);
});
