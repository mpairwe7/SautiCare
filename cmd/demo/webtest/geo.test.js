import { test } from "node:test";
import assert from "node:assert/strict";
import { projector, layout, summary, short } from "../web/geo.js";

// Two wards of the placeholder gazetteer (config/gated/gazetteer.yaml).
const map = {
  wards: [{ id: "w01", name: "Test Ward 1", lat: 0.38, lon: 32.556 }, { id: "w03", name: "Test Ward 3", lat: 0.372, lon: 32.565 }],
  landmarks: [
    { id: "w01_market", ward: "w01", name: "Test Market 1", lat: 0.377, lon: 32.5572 },
    { id: "w01_clinic", ward: "w01", name: "Test Clinic 1", lat: 0.3785, lon: 32.5548 },
    { id: "w03_market", ward: "w03", name: "Test Market 3", lat: 0.369, lon: 32.5662 },
  ],
};
const phones = [
  { id: "caller-a", label: "Caller A", kind: "caller" },
  { id: "vht-1", label: "VHT 1", kind: "responder", role: "vht", wardId: "w01" },
  { id: "transporter-1", label: "Transporter 1", kind: "responder", role: "transporter", wardId: "w01" },
];
const inc = (id, extra) => ({ id, reference: "ref " + id, severity: "red", state: "dispatching", alerts: [], ...extra });

test("the projection keeps north up and a metre east as long as a metre north", () => {
  const P = projector(map.landmarks, 300, 20);
  assert.ok(P.y(0.38) < P.y(0.37), "north is up");
  assert.ok(P.x(32.566) > P.x(32.555), "east is right");
  const k = Math.cos((((0.369 + 0.3785) / 2) * Math.PI) / 180);
  const north = P.y(0.370) - P.y(0.371);
  const east = P.x(32.556 + 0.001 / k) - P.x(32.556);
  assert.ok(Math.abs(north - east) < 1e-6, `${north} vs ${east}`);
  assert.equal(P.x(32.5548), 20, "the westmost point sits on the padding");
});

test("incidents sit at their landmark, else their ward's centre; without a place they are counted, not drawn", () => {
  const board = { incidents: [inc("a", { wardId: "w01", landmarkId: "w01_clinic" }), inc("b", { wardId: "w03", landmarkId: "other" }),
    inc("c", { wardId: "", landmarkId: "" })] };
  const lay = layout(map, phones, board);
  const P = projector([...map.wards, ...map.landmarks], 360, 40);
  const a = lay.incidents.find((i) => i.id === "a");
  const b = lay.incidents.find((i) => i.id === "b");
  assert.deepEqual([a.x, a.y, a.place], [P.x(32.5548), P.y(0.3785), "Test Clinic 1"]);
  assert.deepEqual([b.x, b.y, b.place], [P.x(32.565), P.y(0.372), "Test Ward 3"]);
  assert.equal(lay.incidents.length, 2);
  assert.equal(lay.unplaced, 1);
});

test("incidents at one landmark are spread apart around it", () => {
  const board = { incidents: [inc("a", { landmarkId: "w01_clinic" }), inc("b", { landmarkId: "w01_clinic" }), inc("c", { landmarkId: "w01_clinic" })] };
  const lay = layout(map, phones, board);
  const P = projector([...map.wards, ...map.landmarks], 360, 40);
  const pos = lay.incidents.map((i) => `${i.x.toFixed(1)},${i.y.toFixed(1)}`);
  assert.equal(new Set(pos).size, 3, "no two pins overlap");
  for (const i of lay.incidents) assert.ok(Math.hypot(i.x - P.x(32.5548), i.y - P.y(0.3785)) <= 12, "each stays by the landmark");
});

test("responders sit inside their ward, and each alert draws one line styled by its outcome", () => {
  const board = { incidents: [inc("a", { landmarkId: "w01_market", alerts: [
    { id: "x1", responder: "VHT 1", outcome: "accepted" }, { id: "x2", responder: "Transporter 1", outcome: "pending" },
    { id: "x3", responder: "VHT 9", outcome: "pending" }] })] };
  const lay = layout(map, phones, board);
  const w1 = lay.wards.find((w) => w.id === "w01");
  assert.deepEqual(lay.responders.map((r) => r.short), ["V1", "T1"], "callers are not responders");
  for (const r of lay.responders) assert.ok(r.x > w1.x && r.x < w1.x + w1.w && r.y > w1.y && r.y < w1.y + w1.h, `${r.label} inside Test Ward 1`);
  assert.deepEqual(lay.links.map((l) => [l.responder, l.outcome]), [["VHT 1", "accepted"], ["Transporter 1", "pending"]],
    "an alert to a responder with no phone on screen draws nothing");
  assert.match(summary(lay), /1 open incident on the map: 1 red\. Responders: 2 in Test Ward 1, 0 in Test Ward 3\. 2 alert lines: 1 accepted, 1 pending\./);
});

test("short labels name the role's initial and the number", () => {
  assert.deepEqual(["VHT 3", "Transporter 12"].map(short), ["V3", "T12"]);
});
