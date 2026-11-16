import { test } from "node:test";
import assert from "node:assert/strict";
import { feedItems, serviceOf } from "../web/feed.js";

const at = (s) => new Date(Date.UTC(2026, 10, 16, 9, 0, s)).toISOString();
const ev = (seq, type, s, payload, relayedAt) => ({ seq, type, at: at(s), actor: "system", payload: JSON.stringify(payload), relayedAt });

const board = {
  incidents: [{
    id: "inc-1", reference: "853 402 4461",
    alerts: [{ id: "al-1", responder: "VHT 1", role: "vht" }],
    events: [
      ev(1, "session.started", 0, { channel: "ussd_at" }, at(1)),
      ev(2, "screen.answered", 1, { field: "ward", value: "w01", screen: 6 }),
      ev(3, "alert.issued", 3, { alert: "al-1", ring: 1, wave: 1 }),
      ev(4, "responder.accepted", 7, { alert: "al-1" }),
      ev(5, "incident.escalated", 8, { reason: "no_eligible_responder", role: "transporter" }),
    ],
  }],
};
const sms = { "vht-1": [{ at: at(4), text: "PENDING-SMS Sauti 853 402 4461: RED Injury or accident." }] };
const phones = [{ id: "vht-1", label: "VHT 1", kind: "responder" }];
const map = { wards: [{ id: "w01", name: "Test Ward 1" }], landmarks: [] };

test("the feed lists events and SMS newest first, each tagged with its service and incident", () => {
  const items = feedItems(board, sms, phones, map);
  assert.deepEqual(items.map((i) => [i.service, i.text]), [
    ["dispatch-worker", "Escalated to a coordinator: no eligible responder (transporter)"],
    ["intake-gw", "VHT 1 accepted"],
    ["SMS", "To VHT 1: PENDING-SMS Sauti 853 402 4461: RED Injury or accident."],
    ["dispatch-worker", "Alert to VHT 1: ring 1, wave 1"],
    ["intake-gw", "Screen 6, ward: Test Ward 1"],
    ["intake-gw", "Call started on USSD"],
  ]);
  assert.ok(items.every((i) => i.ref === "853 402 4461" && i.incident === "inc-1"), "the SMS is tied to its incident by its reference");
  assert.equal(items.at(-1).relayedAt, at(1), "an event sealed by outbox-relay says when");
  assert.equal(new Set(items.map((i) => i.key)).size, items.length, "keys are unique");
});

test("an escalation's reason names the service that wrote it", () => {
  assert.equal(serviceOf("incident.escalated", { reason: "dispatch_suspended" }), "intake-gw");
  assert.equal(serviceOf("incident.escalated", { reason: "red_no_acceptance" }), "dispatch-worker");
  assert.equal(serviceOf("alert.declined", {}), "intake-gw", "a decline comes through the responder menu");
  assert.equal(serviceOf("alert.no_response", {}), "dispatch-worker");
});

test("a payload that is not JSON still gives an item", () => {
  const masked = { incidents: [{ id: "i", reference: "1", alerts: [], events: [{ seq: 1, type: "incident.flagged", at: at(0), payload: "{[number]" }] }] };
  assert.deepEqual(feedItems(masked, {}, [], map).map((i) => [i.service, i.text.startsWith("Flagged")]), [["intake-gw", true]]);
});
