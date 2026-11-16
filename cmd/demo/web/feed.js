// The activity feed: every incident event and every SMS on the board, newest
// first, each tagged with the service that produced it. Pure.
import { REF, payload, words } from "./format.js";

// Which service writes which event. intake-gw runs Tool 1, the T2 merge
// check (internal/intake/dedup.go) and the responder menu; dispatch-worker
// runs the cascade (internal/dispatch). An escalation's reason names its
// writer: these few come from intake-gw (internal/intake), the rest from
// dispatch-worker.
const INTAKE = new Set(["session.started", "session.resumed", "screen.answered", "incident.classified", "incident.dispatch_ready",
  "incident.merged", "incident.merge_absorbed", "incident.severity_raised", "incident.flagged", "responder.accepted", "alert.declined"]);
const INTAKE_ESCALATIONS = new Set(["triage_rule_error", "dispatch_suspended", "out_of_area_after_danger", "abandoned_after_danger_yes",
  "abandoned_red_before_dispatch", "dispatch_stalled", "merged_call_escalated"]);

// The steps after an acceptance and the coordinator's close are recorded by
// the demo itself (/api/progress, a preview of M3 and of the M5 console).
const DEMO_APP = new Set(["responder.arrived", "responder.departed", "responder.at_facility", "incident.closed"]);

export function serviceOf(type, p) {
  if (INTAKE.has(type)) return "intake-gw";
  if (DEMO_APP.has(type)) return "responder app";
  if (type === "incident.escalated" && INTAKE_ESCALATIONS.has(p.reason)) return "intake-gw";
  return "dispatch-worker";
}

const FIELDS = { language: "language", danger: "danger signs", category: "what happened", age_band: "age", specific_danger: "danger question",
  ward: "ward", landmark: "nearest place", callback: "call-back number" };

// One sentence per event type; c names alerts, incidents and places.
const SAY = {
  "session.started": () => "Call started on USSD",
  "session.resumed": () => "Dropped call resumed where it stopped",
  "screen.answered": (p, c) => `Screen ${p.screen}, ${FIELDS[p.field] || words(p.field)}: ${c.place(p.value)}`,
  "incident.classified": (p) => `Triaged ${String(p.severity).toUpperCase()} by rule ${p.rule_id}`,
  "incident.dispatch_ready": (p) => `Ready to dispatch, located by ${words(p.location_quality)}`,
  "alert.issued": (p, c) => `Alert to ${c.alert(p.alert)}: ring ${p.ring}, wave ${p.wave}`,
  "responder.accepted": (p, c) => `${c.alert(p.alert)} accepted`,
  "alert.declined": (p, c) => `${c.alert(p.alert)} declined` + (p.reason ? `: ${words(p.reason)}` : ""),
  "alert.no_response": (p, c) => `No answer from ${c.alert(p.alert)}`,
  "alert.stood_down": (p, c) => `${c.alert(p.alert)} stood down, no longer needed`,
  "incident.escalated": (p) => `Escalated to a coordinator: ${words(p.reason)}` + (p.role ? ` (${p.role})` : ""),
  "incident.merged": (p, c) => `Merged into incident ${c.ref(p.into)}`,
  "incident.merge_absorbed": (p, c) => `Call ${c.ref(p.from)} merged into this one`,
  "incident.severity_raised": (p) => `Severity raised to ${String(p.severity).toUpperCase()}`,
  "incident.flagged": (p) => `Flagged: ${words(p.flag)}`,
  "caller.notified": (p) => `Caller sent the notice: ${words(p.kind)}`,
  "responder.arrived": (p) => `${p.role === "transporter" ? "Transporter" : "VHT"} arrived on scene (T5)`,
  "responder.departed": () => "Left for the facility with the patient (T6)",
  "responder.at_facility": (p, c) => `Arrived at ${c.place(p.facility)} (T7)`,
  "incident.closed": (p) => `Incident closed: ${words(p.reason)}`,
};

// feedItems lists the feed, newest first. map is /api/state's gazetteer map,
// phones its phone list (for the SMS recipients' labels).
export function feedItems(board, sms, phones, map) {
  const places = {};
  for (const p of [...(map?.wards || []), ...(map?.landmarks || []), ...(map?.facilities || [])]) places[p.id] = p.name;
  const refs = Object.fromEntries(board.incidents.map((inc) => [inc.id, inc.reference]));
  const byRef = Object.fromEntries(board.incidents.map((inc) => [inc.reference, inc.id]));
  const items = [];
  for (const inc of board.incidents) {
    const alerts = Object.fromEntries(inc.alerts.map((a) => [a.id, a.responder]));
    const c = { alert: (id) => alerts[id] || "a responder", ref: (id) => refs[id] || "an earlier call", place: (v) => places[v] || words(v) };
    for (const e of inc.events) {
      const p = payload(e);
      items.push({ key: inc.id + ":" + e.seq, at: e.at, service: serviceOf(e.type, p), ref: inc.reference, incident: inc.id,
        text: (SAY[e.type] || (() => words(e.type)))(p, c), relayedAt: e.relayedAt || null });
    }
  }
  const labels = Object.fromEntries((phones || []).map((p) => [p.id, p.label]));
  // The arrival check: each question to a caller, and their answer.
  const refOf = Object.fromEntries(board.incidents.map((i) => [i.id, i.reference]));
  for (const a of board.arrival || []) {
    (a.asks || []).forEach((q, n) => {
      const who = labels[a.phone] || a.phone;
      items.push({ key: `arrival:${a.incident}:${n}`, at: q.at, service: "Arrival check", ref: refOf[a.incident] || "", incident: a.incident,
        text: `Asked ${who}: has a health worker arrived yet?`, relayedAt: null });
      if (q.answeredAt) {
        const said = q.answer === "yes" ? "Yes, help arrived" : "No, still waiting";
        items.push({ key: `arrival:${a.incident}:${n}:answer`, at: q.answeredAt, service: "Arrival check", ref: refOf[a.incident] || "", incident: a.incident,
          text: `${who} answered on ${q.via === "ussd" ? "USSD" : "the app"}: ${said}.${q.did ? " Sauti " + q.did + "." : ""}`, relayedAt: null });
      }
    });
  }
  for (const [phone, list] of Object.entries(sms || {})) {
    for (const m of list) {
      const ref = (REF.exec(m.text) || [""])[0];
      items.push({ key: "sms:" + phone + ":" + m.at, at: m.at, service: "SMS", ref, incident: byRef[ref] || "",
        text: `To ${labels[phone] || phone}: ${m.text}`, relayedAt: null });
    }
  }
  // Ascending, then reversed: a stable sort keeps same-instant items in
  // the order they were written, so the reverse lists the latest first.
  return items.sort((a, b) => Date.parse(a.at) - Date.parse(b.at)).reverse();
}
