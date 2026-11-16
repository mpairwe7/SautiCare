import { test } from "node:test";
import assert from "node:assert/strict";
import { layout, located, homeFix, summary, projector, SQUARE, FAC, PIN_R } from "../web/geo.js";

// The placeholder gazetteer's wards, a few landmarks, two stages and its
// three health facilities (config/gated/gazetteer.yaml).
const map = {
  wards: [
    { id: "w01", name: "Test Ward 1", lat: 0.38, lon: 32.556 },
    { id: "w02", name: "Test Ward 2", lat: 0.388, lon: 32.562 },
    { id: "w03", name: "Test Ward 3", lat: 0.372, lon: 32.565 },
    { id: "w04", name: "Test Ward 4", lat: 0.365, lon: 32.554 },
  ],
  landmarks: [
    { id: "w01_clinic", ward: "w01", name: "Test Clinic 1", lat: 0.3785, lon: 32.5548 },
    { id: "w01_school", ward: "w01", name: "Test School 1", lat: 0.38, lon: 32.5572 },
    { id: "w02_market", ward: "w02", name: "Test Market 2", lat: 0.385, lon: 32.5632 },
    { id: "w03_market", ward: "w03", name: "Test Market 3", lat: 0.369, lon: 32.5662 },
    { id: "w04_market", ward: "w04", name: "Test Market 4", lat: 0.362, lon: 32.5552 },
  ],
  stages: [
    { id: "w01_stage_a", ward: "w01", name: "Test Stage 1A", lat: 0.381, lon: 32.557 },
    { id: "w02_stage_a", ward: "w02", name: "Test Stage 2A", lat: 0.389, lon: 32.563 },
  ],
  facilities: [
    { id: "hc_test_1", name: "Test Health Centre 1", lat: 0.383, lon: 32.559 },
    { id: "hc_test_2", name: "Test Health Centre 2", lat: 0.369, lon: 32.56 },
    { id: "hc_test_3", name: "Test Clinic 3", lat: 0.376, lon: 32.568 },
  ],
};
const phones = [
  { id: "vht-1", label: "VHT 1", kind: "responder", role: "vht", wardId: "w01", stage: "w01_stage_a" },
  { id: "vht-3", label: "VHT 3", kind: "responder", role: "vht", wardId: "w01", stage: "w01_stage_a" },
  { id: "transporter-1", label: "Transporter 1", kind: "responder", role: "transporter", wardId: "w01", stage: "w01_stage_a" },
  { id: "vht-4", label: "VHT 4", kind: "responder", role: "vht", wardId: "w02", stage: "w02_stage_a" },
  { id: "caller-a", label: "Caller A", kind: "caller" },
];
const SIZE = { width: 900, height: 640 };
const overlap = (a, b) => a[0] < b[2] && b[0] < a[2] && a[1] < b[3] && b[1] < a[3];
const sq = (r) => [r.x - SQUARE / 2, r.y - SQUARE / 2, r.x + SQUARE / 2, r.y + SQUARE / 2];

test("a smartphone's GPS starts by its responder's stage, each responder of a stage on their own side of it", () => {
  const fixes = ["vht-1", "vht-3", "transporter-1"].map((id) => homeFix(phones.find((p) => p.id === id), phones, map));
  for (const f of fixes) assert.ok(Math.abs(f.lat - 0.381) < 0.0005 && Math.abs(f.lon - 32.557) < 0.0005, JSON.stringify(f));
  assert.equal(new Set(fixes.map((f) => `${f.lat.toFixed(6)},${f.lon.toFixed(6)}`)).size, 3, "three responders, three starting points");
  assert.equal(homeFix({ id: "x", stage: "nowhere" }, phones, map), null);
});

test("smartphone responders are placed at their fix; a basic-phone VHT at the place they sent, while the incident is open", () => {
  const fix = (p) => homeFix(p, phones, map);
  const board = { incidents: [{ id: "i1", state: "assigned" }, { id: "i2", state: "closed" }],
    shares: [{ incident: "i1", vht: "VHT 1", lat: 0.3785, lon: 32.5548, landmark: "Test Clinic 1" }, { incident: "i2", vht: "VHT 4", lat: 0.385, lon: 32.5632, landmark: "Test Market 2" }] };
  const basic = located(phones, { vht: "basic", transporter: "smart" }, fix, board);
  assert.deepEqual(Object.keys(basic).sort(), ["Transporter 1", "VHT 1"], "VHT 4's share is for a closed incident");
  assert.equal(basic["VHT 1"].how, "shared");
  assert.equal(basic["VHT 1"].place, "Test Clinic 1");
  assert.equal(basic["Transporter 1"].how, "phone");
  const smart = located(phones, { vht: "smart", transporter: "basic" }, fix, board);
  assert.deepEqual(Object.keys(smart).sort(), ["VHT 1", "VHT 3", "VHT 4"]);
  assert.ok(Object.values(smart).every((w) => w.how === "phone"));
});

test("the map marks every health facility at its point, and draws located responders apart, clear of the pins, outside the ward groups", () => {
  const where = located(phones, { vht: "smart", transporter: "smart" }, (p) => homeFix(p, phones, map), { incidents: [] });
  const board = { incidents: [{ id: "i1", reference: "713 517 2801", severity: "red", state: "assigned", landmarkId: "w01_school", alerts: [], t: {} }] };
  const lay = layout(map, phones, board, { ...SIZE, where });
  const P = projector([...map.wards, ...map.landmarks], SIZE.width, SIZE.height, 56);
  assert.equal(lay.facilities.length, 3);
  for (const f of lay.facilities) {
    const src = map.facilities.find((x) => x.id === f.id);
    assert.ok(Math.abs(f.x - P.x(src.lon)) < 1e-6 && Math.abs(f.y - P.y(src.lat)) < 1e-6, f.id);
    assert.ok(f.x - FAC / 2 >= 0 && f.x + FAC / 2 <= SIZE.width, `${f.id} is inside the frame`);
  }
  const placed = lay.responders.filter((r) => r.located);
  assert.equal(placed.length, 4, "every responder is located on smartphones");
  const pin = lay.incidents[0];
  const pinBox = [pin.x - PIN_R, pin.y - PIN_R, pin.x + PIN_R, pin.y + PIN_R];
  placed.forEach((r, i) => {
    assert.ok(!overlap(sq(r), pinBox), `${r.label} clears the pin`);
    placed.slice(i + 1).forEach((o) => assert.ok(!overlap(sq(r), sq(o)), `${r.label} and ${o.label} do not overlap`));
  });
  assert.equal(lay.responders.filter((r) => !r.located).length, 0, "no ward group repeats a located responder");
  assert.match(summary(lay), /^4 wards, 5 landmarks and 3 health facilities: Test Health Centre 1, Test Health Centre 2, Test Clinic 3\. .*Responders: 3 in Test Ward 1, 1 in Test Ward 2, 0 in Test Ward 3, 0 in Test Ward 4; 4 shown where they are\./);
});
