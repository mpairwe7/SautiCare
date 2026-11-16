import { test } from "node:test";
import assert from "node:assert/strict";
import { stageStats, incidentStages, PHASES } from "../web/analytics.js";

const at = (s) => new Date(Date.UTC(2026, 10, 16, 12, 0, s)).toISOString();
const inc = (id, t, over) => ({ id, reference: id, severity: "red", state: "closed", t, alerts: [], escalations: [], events: [], ...over });

test("each stage's median, fastest and slowest, over the incidents that completed it", () => {
  const list = [
    inc("a", { T1: at(0), T2: at(60), T3: at(62), T4: at(80) }),
    inc("b", { T1: at(0), T2: at(40), T3: at(41), T4: at(141) }),
    inc("c", { T1: at(0), T2: at(50) }, { state: "dispatching" }),
    inc("m", { T1: at(0), T2: at(5) }, { state: "merged" }),
  ];
  const s = stageStats(list);
  assert.equal(s.length, PHASES.length);
  assert.deepEqual(s.map((x) => x.label).slice(0, 3), ["Intake", "Finding responders", "Waiting for acceptance"]);
  assert.deepEqual([s[0].n, s[0].median, s[0].min, s[0].max], [3, 50000, 40000, 60000], "a merged call is not counted twice");
  assert.deepEqual([s[2].n, s[2].median], [2, 59000]);
  assert.deepEqual([s[4].n, s[4].median, s[4].min], [0, null, null]);
});

test("each incident's stages on one axis, newest first, the open one running to now", () => {
  const now = Date.parse(at(200));
  const { rows, span, ticks } = incidentStages([
    inc("open", { T1: at(100), T2: at(130) }, { state: "dispatching" }),
    inc("done", { T1: at(0), T2: at(60), T3: at(62), T4: at(80) }),
    inc("merged", { T1: at(0), T2: at(5) }, { state: "merged" }),
    inc("blank", {}, { state: "intake" }),
  ], now);
  assert.deepEqual(rows.map((r) => r.id), ["open", "done"]);
  assert.deepEqual(rows[0].segments.map((s) => [s.slot, s.ms, s.open]), [[0, 30000, false], [1, 70000, true]]);
  assert.equal(rows[1].total, 80000);
  assert.equal(span, 100000);
  assert.equal(ticks[0], 0);
  assert.equal(incidentStages([inc("x", { T1: at(0), T2: at(1) })], now, 0).rows.length, 0, "limit");
});
