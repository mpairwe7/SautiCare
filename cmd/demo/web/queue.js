// The live queue: every incident on the board in one list, most urgent first,
// grouped the way a dispatcher works it. Pure: the view renders what these
// return.
import { ended } from "./format.js";

export const GROUPS = ["waiting", "assigned", "escalated", "done"];
export const GROUP_NAMES = { all: "All", waiting: "Waiting", assigned: "Assigned", escalated: "Escalated", done: "Done" };
const RANK = { red: 0, yellow: 1, green: 2, "": 3, none: 3 };

// OVERDUE: a call waits too long for an acceptance after this long, the
// red escalation threshold of config/gated/dispatch.yaml (3 min).
export const OVERDUE_MS = 3 * 60 * 1000;

// groupOf: done once at the facility, closed or merged; escalated while a
// coordinator has been called; assigned once someone accepted; else waiting.
export function groupOf(inc) {
  if (ended(inc) || inc.state === "at_facility") return "done";
  if (inc.escalated) return "escalated";
  if (["assigned", "on_scene", "transporting"].includes(inc.state)) return "assigned";
  return "waiting";
}

const t1 = (inc) => (inc.t.T1 ? Date.parse(inc.t.T1) : Infinity);
const lastT = (inc) => Math.max(0, ...Object.values(inc.t).map((v) => Date.parse(v)));

// order sorts open incidents by severity, then the longest wait first; done
// ones follow, the most recently finished first.
export function order(incidents) {
  return incidents.slice().sort((a, b) => {
    const da = groupOf(a) === "done";
    const db = groupOf(b) === "done";
    if (da !== db) return da ? 1 : -1;
    if (da) return lastT(b) - lastT(a);
    return (RANK[a.severity] ?? 3) - (RANK[b.severity] ?? 3) || t1(a) - t1(b);
  });
}

// counts returns the number of incidents per group, and all.
export function counts(incidents) {
  const out = Object.fromEntries(["all", ...GROUPS].map((g) => [g, 0]));
  for (const inc of incidents) {
    out.all++;
    out[groupOf(inc)]++;
  }
  return out;
}

export const filtered = (incidents, group) => order(incidents).filter((inc) => group === "all" || groupOf(inc) === group);

// overdue: open, nobody has accepted, and the call is older than OVERDUE_MS.
export function overdue(inc, now) {
  return groupOf(inc) !== "done" && !inc.t.T4 && inc.t.T1 !== undefined && now - Date.parse(inc.t.T1) > OVERDUE_MS;
}

// roleCell says where one role stands on an incident: who accepted, who is
// being alerted, an escalation, or nothing yet. tone picks the text colour.
export function roleCell(inc, role) {
  const mine = inc.alerts.filter((a) => a.role === role);
  const took = mine.find((a) => a.outcome === "accepted");
  if (took) {
    const ms = took.respondedAt ? Date.parse(took.respondedAt) - Date.parse(took.sentAt) : null;
    return { tone: "accepted", who: took.responder, text: "accepted", ms };
  }
  if (inc.escalations.some((e) => e.role === role)) return { tone: "declined", who: "", text: "escalated", ms: null };
  const pending = mine.filter((a) => a.outcome === "pending");
  if (pending.length) return { tone: "pending", who: pending.map((a) => a.responder).join(", "), text: "alerted", ms: null };
  if (mine.length) return { tone: "declined", who: "", text: "no answer yet", ms: null };
  if (ended(inc)) return { tone: "none", who: "", text: "—", ms: null };
  // Dispatch is looking, but every responder in reach is busy or holds
  // another call's alert (one alert per responder, ADR-0044).
  if (["dispatching", "assigned", "on_scene"].includes(inc.state)) return { tone: "pending", who: "", text: "waiting for a free responder", ms: null };
  return { tone: "none", who: "", text: "not alerted yet", ms: null };
}
