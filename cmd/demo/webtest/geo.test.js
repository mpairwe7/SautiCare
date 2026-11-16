import { test } from "node:test";
import assert from "node:assert/strict";
import { projector, cells, layout, summary, short, PIN_R } from "../web/geo.js";

// The placeholder gazetteer (config/gated/gazetteer.yaml): ward centres and
// two landmarks each.
const map = {
  wards: [
    { id: "w01", name: "Test Ward 1", lat: 0.38, lon: 32.556 },
    { id: "w02", name: "Test Ward 2", lat: 0.388, lon: 32.562 },
    { id: "w03", name: "Test Ward 3", lat: 0.372, lon: 32.565 },
    { id: "w04", name: "Test Ward 4", lat: 0.365, lon: 32.554 },
  ],
  landmarks: [
    { id: "w01_market", ward: "w01", name: "Test Market 1", lat: 0.377, lon: 32.5572 },
    { id: "w01_clinic", ward: "w01", name: "Test Clinic 1", lat: 0.3785, lon: 32.5548 },
    { id: "w02_market", ward: "w02", name: "Test Market 2", lat: 0.385, lon: 32.5632 },
    { id: "w02_well", ward: "w02", name: "Test Well 2", lat: 0.391, lon: 32.56 },
    { id: "w03_market", ward: "w03", name: "Test Market 3", lat: 0.369, lon: 32.5662 },
    { id: "w03_well", ward: "w03", name: "Test Well 3", lat: 0.375, lon: 32.563 },
    { id: "w04_market", ward: "w04", name: "Test Market 4", lat: 0.362, lon: 32.5552 },
    { id: "w04_well", ward: "w04", name: "Test Well 4", lat: 0.368, lon: 32.552 },
  ],
};
const phones = [
  { id: "caller-a", label: "Caller A", kind: "caller" },
  ...["VHT 1", "VHT 2", "VHT 3", "Transporter 1", "Transporter 2"].map((label) =>
    ({ id: label, label, kind: "responder", role: label[0] === "V" ? "vht" : "transporter", wardId: "w01" })),
  { id: "vht-4", label: "VHT 4", kind: "responder", role: "vht", wardId: "w02" },
  { id: "transporter-3", label: "Transporter 3", kind: "responder", role: "transporter", wardId: "w02" },
];
const inc = (id, extra) => ({ id, reference: "713 517 28" + id.padStart(2, "0"), severity: "red", state: "dispatching", alerts: [], t: {}, ...extra });
const at = (s) => ({ T1: `2026-11-16T09:00:${String(s).padStart(2, "0")}Z` });
const SIZE = { width: 900, height: 640 };

// contains: (x, y) is inside the polygon, by the even-odd rule.
function contains(poly, x, y) {
  let odd = false;
  poly.forEach(([x1, y1], i) => {
    const [x2, y2] = poly[(i + 1) % poly.length];
    if ((y1 > y) !== (y2 > y) && x < x1 + ((y - y1) * (x2 - x1)) / (y2 - y1)) odd = !odd;
  });
  return odd;
}
const area = (poly) => Math.abs(poly.reduce((s, [x1, y1], i) => {
  const [x2, y2] = poly[(i + 1) % poly.length];
  return s + x1 * y2 - x2 * y1;
}, 0)) / 2;
const overlap = (a, b) => a[0] < b[2] && b[0] < a[2] && a[1] < b[3] && b[1] < a[3];

test("the projection keeps north up, a metre east as long as a metre north, and centres the places in the frame", () => {
  const P = projector(map.landmarks, 300, 900, 20);
  assert.ok(P.y(0.38) < P.y(0.37), "north is up");
  assert.ok(P.x(32.566) > P.x(32.555), "east is right");
  const k = Math.cos((((0.362 + 0.391) / 2) * Math.PI) / 180);
  const north = P.y(0.370) - P.y(0.371);
  const east = P.x(32.556 + 0.001 / k) - P.x(32.556);
  assert.ok(Math.abs(north - east) < 1e-6, `${north} vs ${east}`);
  assert.ok(Math.abs(P.x(32.552) - 20) < 1e-9, "a narrow frame: the westmost place sits on the padding");
  assert.ok(Math.abs(P.y(0.391) - (900 - P.y(0.362))) < 1e-9, "and the places are centred north to south");
  const W = projector(map.landmarks, 1200, 500, 20);
  assert.ok(Math.abs(W.y(0.391) - 20) < 1e-9, "a wide frame: the northmost place sits on the padding");
  assert.ok(Math.abs(W.x(32.552) - (1200 - W.x(32.5662))) < 1e-9, "and the places are centred east to west");
});

test("each ward's territory is the part of the frame nearest its centre, and the territories tile the frame", () => {
  const sites = [{ x: 120, y: 300 }, { x: 400, y: 80 }, { x: 520, y: 420 }, { x: 90, y: 560 }];
  const frame = [0, 0, 900, 640];
  const polys = cells(sites, frame);
  polys.forEach((poly, i) => assert.ok(contains(poly, sites[i].x, sites[i].y), `cell ${i} contains its own site`));
  const total = polys.reduce((s, p) => s + area(p), 0);
  assert.ok(Math.abs(total - 900 * 640) < 1e-6, `the areas sum to the frame's: ${total}`);
  for (let x = 5; x < 900; x += 37) {
    for (let y = 5; y < 640; y += 37) {
      const d = sites.map((s) => Math.hypot(s.x - x, s.y - y));
      const near = d.indexOf(Math.min(...d));
      assert.ok(contains(polys[near], x, y), `(${x}, ${y}) is in the territory of site ${near}, its nearest`);
    }
  }
});

test("the map's territories fill the panel and hold their own ward centre", () => {
  const lay = layout(map, phones, { incidents: [] }, SIZE);
  const total = lay.wards.reduce((s, w) => s + area(w.cell), 0);
  assert.ok(Math.abs(total - 900 * 640) < 1e-6, `${total}`);
  for (const w of lay.wards) assert.ok(contains(w.cell, w.cx, w.cy), `${w.name} holds its centre`);
});

test("incidents sit at their landmark, else their ward's centre; without a place they are counted, not drawn", () => {
  const board = { incidents: [inc("a", { wardId: "w01", landmarkId: "w01_clinic" }), inc("b", { wardId: "w03", landmarkId: "other" }),
    inc("c", { wardId: "", landmarkId: "" })] };
  const lay = layout(map, phones, board, SIZE);
  const P = projector([...map.wards, ...map.landmarks], 900, 640, 56);
  const a = lay.incidents.find((i) => i.id === "a");
  const b = lay.incidents.find((i) => i.id === "b");
  assert.deepEqual([a.x, a.y, a.place, a.at], [P.x(32.5548), P.y(0.3785), "Test Clinic 1", null]);
  assert.deepEqual([b.x, b.y, b.place], [P.x(32.565), P.y(0.372), "Test Ward 3"]);
  assert.equal(lay.incidents.length, 2);
  assert.equal(lay.unplaced, 1);
});

test("incidents at one landmark fan out a pin apart, in order of their call, each tied to the landmark and labelled clear of the others", () => {
  const P = projector([...map.wards, ...map.landmarks], 900, 640, 56);
  const market = { x: P.x(32.5632), y: P.y(0.385) };
  for (const n of [2, 5, 9]) {
    const list = Array.from({ length: n }, (_, i) => inc(String(i), { landmarkId: "w02_market", t: at(i) }));
    const lay = layout(map, phones, { incidents: list.slice().reverse() }, SIZE);
    const pins = lay.incidents;
    for (const [i, p] of pins.entries()) {
      assert.deepEqual(p.at, market, "a leader runs to the landmark");
      for (const q of pins.slice(i + 1)) assert.ok(Math.hypot(p.x - q.x, p.y - q.y) >= 2 * PIN_R, `${n} pins: ${p.id} and ${q.id} do not overlap`);
      for (const q of pins) {
        if (q !== p) assert.ok(!overlap(p.label.box, q.label.box), `${n} pins: the labels of ${p.id} and ${q.id} do not overlap`);
        const nx = Math.max(q.label.box[0], Math.min(p.x, q.label.box[2]));
        const ny = Math.max(q.label.box[1], Math.min(p.y, q.label.box[3]));
        assert.ok(Math.hypot(p.x - nx, p.y - ny) >= PIN_R, `${n} pins: ${q.id}'s label clears ${p.id}'s pin`);
      }
    }
    const top = pins.reduce((m, p) => (p.y < m.y ? p : m));
    assert.equal(top.id, "0", `${n} pins: the first call sits at the top`);
    const again = layout(map, phones, { incidents: list }, SIZE).incidents;
    for (const p of pins) {
      const q = again.find((x) => x.id === p.id);
      assert.deepEqual([q.x, q.y], [p.x, p.y], "the order of the board does not move a pin");
    }
  }
});

test("each ward's responders sit together in their own territory, clear of every pin, and each alert draws one line styled by its outcome", () => {
  const near = ["w01_market", "w01_clinic", "w02_market", "w02_market", "w02_market", "w02_market", "w02_market"];
  const list = near.map((l, i) => inc(String(i), { landmarkId: l, t: at(i) }));
  list[0].alerts = [{ id: "x1", responder: "VHT 1", outcome: "accepted" }, { id: "x2", responder: "Transporter 1", outcome: "pending" },
    { id: "x3", responder: "VHT 9", outcome: "pending" }];
  const centres = [inc("c1", { wardId: "w01", landmarkId: "", t: at(20) }), inc("c2", { wardId: "w02", landmarkId: "", t: at(21) })];
  // On a phone, a ward with pins on its centre and around it has no room left for its group; at a projector's width it does.
  for (const [size, incidents] of [[SIZE, [...list, ...centres]], [{ width: 320, height: 560 }, list]]) {
    const lay = layout(map, phones, { incidents }, size);
    assert.deepEqual(lay.responders.map((r) => r.short), ["V1", "V2", "V3", "T1", "T2", "V4", "T3"], "callers are not responders");
    for (const r of lay.responders) {
      const w = lay.wards.find((x) => x.name === r.ward);
      for (const [dx, dy] of [[-12, -12], [12, -12], [12, 12], [-12, 12]]) {
        assert.ok(contains(w.cell, r.x + dx, r.y + dy), `${size.width} px: ${r.label}'s square is inside ${r.ward}`);
      }
      for (const p of lay.incidents) assert.ok(Math.hypot(r.x - p.x, r.y - p.y) > PIN_R + 12, `${size.width} px: ${r.label} clears pin ${p.id}`);
      const mates = lay.responders.filter((m) => m.ward === r.ward);
      for (const m of mates) assert.ok(Math.hypot(r.x - m.x, r.y - m.y) < 100, `${size.width} px: ${r.label} sits by ${m.label}`);
    }
    assert.deepEqual(lay.links.map((l) => [l.responder, l.outcome]), [["VHT 1", "accepted"], ["Transporter 1", "pending"]],
      "an alert to a responder with no phone on screen draws nothing");
  }
  const lay = layout(map, phones, { incidents: [list[0]] }, SIZE);
  assert.match(summary(lay), /^4 wards and 8 landmarks\. 1 open incident on the map: 1 red\. Responders: 5 in Test Ward 1, 2 in Test Ward 2, 0 in Test Ward 3, 0 in Test Ward 4\. 2 alert lines: 1 accepted, 1 pending\.$/);
});

test("a landmark's name shows only where it clears the pins, the groups and the other names", () => {
  const list = Array.from({ length: 5 }, (_, i) => inc(String(i), { landmarkId: "w02_market", t: at(i) }));
  const close = [{ id: "w03_a", ward: "w03", name: "Test Stage 3", lat: 0.3735, lon: 32.5665 }, { id: "w03_b", ward: "w03", name: "Test School 3", lat: 0.3738, lon: 32.5665 }];
  const lay = layout({ ...map, landmarks: [...map.landmarks, ...close] }, phones, { incidents: list }, SIZE);
  const shown = lay.landmarks.filter((l) => l.label);
  assert.ok(shown.length >= 7, `most names fit at 900 by 640: ${shown.map((l) => l.name)}`);
  const pins = lay.incidents.flatMap((p) => [[p.x - PIN_R, p.y - PIN_R, p.x + PIN_R, p.y + PIN_R], p.label.box]);
  for (const l of shown) {
    for (const b of pins) assert.ok(!overlap(l.label.box, b), `${l.name} clears the pins`);
    for (const m of shown) if (m !== l) assert.ok(!overlap(l.label.box, m.label.box), `${l.name} clears ${m.name}`);
  }
});

test("short labels name the role's initial and the number", () => {
  assert.deepEqual(["VHT 3", "Transporter 12"].map(short), ["V3", "T12"]);
});
