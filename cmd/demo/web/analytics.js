// The study's analytics: the protocol timestamps T1 to T7 that the pilot
// analyses, as the time spent in each stage. Pure: charts.js draws what
// these return.
import { PHASES, timeline, ticks } from "./metrics.js";

function median(xs) {
  if (!xs.length) return null;
  const s = xs.slice().sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

// A merged call is its survivor's: counting it would count one emergency twice.
const counted = (inc) => inc.state !== "merged" && inc.t.T1;

// stageStats is each protocol stage's median, fastest and slowest time over
// the incidents that completed it, in stage order, and how many did.
export function stageStats(incidents) {
  return PHASES.map((p, slot) => {
    const xs = incidents.filter((i) => counted(i) && i.t[p.from] && i.t[p.to])
      .map((i) => Date.parse(i.t[p.to]) - Date.parse(i.t[p.from])).filter((ms) => ms >= 0);
    return { slot, label: p.label, from: p.from, to: p.to, n: xs.length, median: median(xs),
      min: xs.length ? Math.min(...xs) : null, max: xs.length ? Math.max(...xs) : null };
  });
}

// incidentStages lays out the latest incidents' stages on one shared axis
// from each call (T1): one row per incident, newest first, at most limit.
// The stage an open incident is in runs to now and is marked open.
export function incidentStages(incidents, now, limit = 8) {
  const rows = [];
  for (const inc of incidents) {
    if (!counted(inc) || rows.length >= limit) continue;
    const tl = timeline(inc, now);
    if (!tl || !tl.segments.length) continue;
    rows.push({ id: inc.id, ref: inc.reference, severity: inc.severity || "none", state: inc.state,
      segments: tl.segments.map((s) => ({ slot: PHASES.findIndex((p) => p.from === s.from), label: s.label, start: s.start, ms: s.end - s.start, open: s.open })),
      total: tl.span });
  }
  const span = Math.max(1000, ...rows.map((r) => r.total));
  return { rows, span, ticks: ticks(span) };
}

export { PHASES };
