// What each person's phone should show, derived from the board exactly as
// the API returns it: a responder's live alert or job, and a caller's
// progress. It mirrors the responder menu (op.responder_menu_alert): an open
// assignment first, then the newest pending alert, then the newest lapsed
// alert whose role is still open. Pure.
import { ended, payload } from "./format.js";

const LOOKING = new Set(["dispatching", "assigned", "on_scene"]);

// actionable: the alert lapsed, but its incident still looks for help and
// nobody has taken its role, so accepting it still counts (0041, F-26).
export function actionable(inc, a) {
  return a.outcome === "no_response" && LOOKING.has(inc.state) &&
    !inc.alerts.some((x) => x.role === a.role && x.outcome === "accepted");
}

// responderStatus returns {kind, inc, alert, late, deadline, recent}:
//   busy  — the responder accepted an incident that is still open;
//   alert — a pending alert (deadline: when it lapses), or a lapsed one that
//           can still be accepted (late: true);
//   idle  — neither. recent lists every alert the responder had, newest first.
export function responderStatus(board, label, timeoutMs) {
  const mine = [];
  for (const inc of (board && board.incidents) || []) {
    for (const a of inc.alerts) if (a.responder === label) mine.push({ inc, alert: a });
  }
  mine.sort((x, y) => Date.parse(y.alert.sentAt) - Date.parse(x.alert.sentAt));
  const busy = mine.find(({ inc, alert }) => alert.outcome === "accepted" && !ended(inc));
  if (busy) return { kind: "busy", ...busy, late: false, deadline: null, recent: mine };
  const pending = mine.find(({ inc, alert }) => alert.outcome === "pending" && !ended(inc));
  if (pending) return { kind: "alert", ...pending, late: false, deadline: Date.parse(pending.alert.sentAt) + timeoutMs, recent: mine };
  const late = mine.find(({ inc, alert }) => actionable(inc, alert));
  if (late) return { kind: "alert", ...late, late: true, deadline: null, recent: mine };
  return { kind: "idle", inc: null, alert: null, late: false, deadline: null, recent: mine };
}

// arrivedRoles lists the roles whose arrival is recorded on the incident.
export function arrivedRoles(inc) {
  return new Set(inc.events.filter((e) => e.type === "responder.arrived").map((e) => payload(e).role).filter(Boolean));
}

// nextStep is the step a responder of role records next on an accepted
// incident, or "" when there is none: a VHT attests arrival; a transporter
// arrives, leaves with the patient, then reaches the facility.
export function nextStep(inc, role) {
  if (ended(inc) || inc.state === "at_facility") return "";
  const arrived = arrivedRoles(inc).has(role);
  if (!arrived) return inc.state === "assigned" || inc.state === "on_scene" ? "arrived" : "";
  if (role !== "transporter") return "";
  if (!inc.t.T6) return inc.state === "on_scene" ? "departed" : "";
  if (!inc.t.T7) return "at_facility";
  return "";
}

// partner describes the other role on the incident, for a responder's job
// screen: who accepted it, or that it is still being looked for or escalated.
export function partner(inc, role) {
  const other = role === "vht" ? "transporter" : "vht";
  const name = other === "vht" ? "VHT" : "Transporter";
  const took = inc.alerts.find((a) => a.role === other && a.outcome === "accepted");
  if (took && arrivedRoles(inc).has(other)) return { tone: "accepted", text: `${took.responder} is on scene` };
  if (took) return { tone: "accepted", text: `${took.responder} is coming too` };
  if (inc.escalations.some((e) => e.role === other)) return { tone: "declined", text: `No ${name.toLowerCase()} found: a coordinator is arranging one` };
  if (inc.alerts.some((a) => a.role === other && a.outcome === "pending")) return { tone: "pending", text: `Alerting a ${name.toLowerCase()}…` };
  return { tone: "pending", text: `Waiting for a free ${name.toLowerCase()}` };
}

// callerProgress is the caller's tracker: each step, done or not, worded for
// the caller (no responder is named to them).
export function callerProgress(inc) {
  const took = (role) => inc.alerts.some((a) => a.role === role && a.outcome === "accepted");
  const steps = [
    { key: "call", label: "Call received", done: Boolean(inc.t.T1) },
    { key: "search", label: "Finding the nearest helper", done: took("vht") || took("transporter") },
    { key: "vht", label: "A health worker is on the way", done: took("vht") },
    { key: "transport", label: "Transport is on the way", done: took("transporter") },
    { key: "arrived", label: "Help has arrived", done: Boolean(inc.t.T5) },
    { key: "departed", label: "On the way to the health facility", done: Boolean(inc.t.T6) },
    { key: "facility", label: "At the health facility", done: Boolean(inc.t.T7) },
  ];
  const current = steps.find((s) => !s.done);
  // An escalation matters to the caller only while a helper is still missing.
  const missing = !took("vht") || !took("transporter");
  return { steps, current: current ? current.key : "", escalated: inc.escalated && missing && !inc.t.T6, merged: inc.state === "merged" };
}
