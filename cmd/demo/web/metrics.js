// Figures derived from the board and the inboxes, exactly as the API returns
// them. Pure: the views render what these return.
import { ended, payload } from "./format.js";

export const SEVERITIES = ["red", "yellow", "green", "none"];
export const OUTCOMES = ["pending", "accepted", "declined", "no_response", "stood_down"];

const zeros = (keys) => Object.fromEntries(keys.map((k) => [k, 0]));

function median(xs) {
  if (!xs.length) return null;
  const s = xs.slice().sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

// metrics is the live strip: open incidents by severity, the median time
// from call to acceptance (T1 to T4), alerts by outcome, escalations and SMS.
export function metrics(board, sms) {
  const active = zeros(SEVERITIES);
  const outcomes = zeros(OUTCOMES);
  const waits = [];
  let escalations = 0;
  let escalated = 0;
  for (const inc of board.incidents) {
    if (!ended(inc)) active[inc.severity || "none"]++;
    if (inc.t.T1 && inc.t.T4) waits.push(Date.parse(inc.t.T4) - Date.parse(inc.t.T1));
    for (const a of inc.alerts) outcomes[a.outcome] = (outcomes[a.outcome] || 0) + 1;
    escalations += inc.escalations.length;
    if (inc.escalations.length) escalated++;
  }
  const inboxes = Object.values(sms || {});
  return {
    active,
    activeTotal: Object.values(active).reduce((n, x) => n + x, 0),
    acceptance: { median: median(waits), count: waits.length },
    outcomes,
    alerts: Object.values(outcomes).reduce((n, x) => n + x, 0),
    escalations: { count: escalations, incidents: escalated },
    sms: { count: inboxes.reduce((n, list) => n + list.length, 0), phones: inboxes.filter((list) => list.length).length },
  };
}

// The protocol's intervals, each from one timestamp to the next.
export const PHASES = [
  { from: "T1", to: "T2", label: "Intake" },
  { from: "T2", to: "T3", label: "Finding responders" },
  { from: "T3", to: "T4", label: "Waiting for acceptance" },
  { from: "T4", to: "T5", label: "Travelling to the scene" },
  { from: "T5", to: "T6", label: "On scene" },
  { from: "T6", to: "T7", label: "To the facility" },
];

const HOUR = 3600000;
const STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600].map((s) => s * 1000);

// ticks are round times from 0 to span, at most six intervals apart: past
// six hours, a whole number of hours.
export function ticks(span) {
  const step = STEPS.find((s) => span / s <= 6) || Math.ceil(span / 6 / HOUR) * HOUR;
  const out = [];
  for (let t = 0; t <= span; t += step) out.push(t);
  return out;
}

// timeline places an incident's intervals on one axis from T1, in ms. An open
// incident's current interval runs to now and is marked open.
export function timeline(inc, now) {
  if (!inc.t.T1) return null;
  const t1 = Date.parse(inc.t.T1);
  const at = (k) => Date.parse(inc.t[k]) - t1;
  const segments = [];
  for (const p of PHASES) {
    if (!inc.t[p.from]) break;
    if (inc.t[p.to]) {
      segments.push({ ...p, start: at(p.from), end: at(p.to), open: false });
      continue;
    }
    if (!ended(inc)) segments.push({ ...p, start: at(p.from), end: Math.max(at(p.from), now - t1), open: true });
    break;
  }
  const span = Math.max(1000, ...segments.map((s) => s.end));
  const milestones = Object.keys(inc.t).sort().map((key) => ({ key, at: at(key) }));
  return { span, segments, milestones, ticks: ticks(span) };
}

const CLOSING = new Set(["responder.accepted", "alert.declined", "alert.no_response", "alert.stood_down"]);

// cascade groups an incident's alerts into waves, each alert with how long
// it took to end (accepted, declined, no response or stood down), or null
// while it is pending.
export function cascade(inc) {
  const endedAt = {};
  for (const e of inc.events) {
    const p = payload(e);
    if (CLOSING.has(e.type) && p.alert) endedAt[p.alert] = e.at;
  }
  const waves = new Map();
  for (const a of inc.alerts) {
    const end = a.respondedAt || endedAt[a.id];
    if (!waves.has(a.wave)) waves.set(a.wave, []);
    waves.get(a.wave).push({ responder: a.responder, role: a.role, ring: a.ring, outcome: a.outcome, reason: a.reason || "",
      took: end ? Date.parse(end) - Date.parse(a.sentAt) : null });
  }
  return [...waves].sort((a, b) => a[0] - b[0]).map(([wave, alerts]) => ({ wave, alerts }));
}
