// One dispatch-board card per incident: its facts, the response timeline,
// the alert cascade and the full tables.
import { h, svg, clock } from "./dom.js";
import { ended, mmss, words } from "./format.js";
import { timeline, cascade } from "./metrics.js";
import { OUTCOME_NAMES } from "./views.js";

const tNames = { T1: "call", T2: "ready to dispatch", T3: "first alert", T4: "accepted", T5: "arrived", T6: "departed", T7: "at facility" };
const stateNames = { intake: "Taking the call", triaged: "Triaged", dispatching: "Finding help", assigned: "Help assigned",
  on_scene: "On scene", transporting: "Transporting", at_facility: "At facility", closed: "Closed", merged: "Merged" };
const GLYPH = { pending: "…", accepted: "✓", declined: "✕", no_response: "○", stood_down: "↓" };

export const since = (from, to) => mmss(to - from);

function details(open, key, summary, body) {
  const d = h("details", { open: open.has(key), ontoggle: () => (d.open ? open.add(key) : open.delete(key)) },
    h("summary", { text: summary }), body);
  return d;
}

function table(head, rows) {
  return h("table", {}, h("thead", {}, h("tr", {}, head.map((c) => h("th", { scope: "col", text: c })))),
    h("tbody", {}, rows.map((r) => h("tr", {}, r.map((c) => (c instanceof Node ? h("td", {}, c) : h("td", { text: c })))))));
}

function attempts(list) {
  return list.map((a) => a.kind + " by " + a.channel + ": " + a.status + (a.delivery ? " (" + a.delivery + ")" : "")).join("; ") || "—";
}

const TEXT_W = 6.4; // px per character at the chart's 11px labels

// timelineChart draws an incident's intervals from T1 as one row of
// blocks, sized to width px. The T-times list under it is its table view.
export function timelineChart(inc, now, width) {
  const tl = timeline(inc, now);
  if (!tl) return null;
  const W = Math.max(200, width);
  const x = (t) => (t / tl.span) * W;
  const blocks = tl.segments.map((s) => {
    const x0 = x(s.start);
    const w = Math.max(2, x(s.end) - x0 - 2);
    const text = `${s.label} ${mmss(s.end - s.start)}`;
    return svg("g", { class: "tl-seg" + (s.open ? " open" : "") },
      svg("title", { text: `${s.label}: ${s.from} to ${s.open ? "now" : s.to}, ${mmss(s.end - s.start)}` }),
      svg("rect", { x: x0.toFixed(1), y: "2", width: w.toFixed(1), height: "18", rx: "4" }),
      text.length * TEXT_W + 12 <= w ? svg("text", { x: (x0 + 6).toFixed(1), y: "15", text }) : null);
  });
  const ticks = tl.ticks.map((t, i) => svg("text", { class: "tl-tick", x: x(t).toFixed(1), y: "36",
    "text-anchor": i === 0 ? "start" : t === tl.span || x(t) > W - 16 ? "end" : "middle", text: mmss(t) }));
  const open = tl.segments.find((s) => s.open);
  const label = tl.segments.map((s) => `${s.label} ${mmss(s.end - s.start)}${s.open ? " so far" : ""}`).join(", ");
  return svg("svg", { class: "tl", viewBox: `0 0 ${W} 40`, width: "100%", height: "40", role: "img",
    "aria-label": `Response timeline from the call: ${label}.${open ? "" : " Ended."}` }, blocks, ticks);
}

// cascadeView shows the alert waves as columns, one chip per alert, and the
// escalations to a coordinator as a last column.
function cascadeView(inc) {
  const waves = cascade(inc);
  if (!waves.length && !inc.escalations.length) return null;
  const cols = waves.map((w) => h("section", { class: "wave" }, h("h4", { text: "Wave " + w.wave }),
    h("ul", {}, w.alerts.map((a) => h("li", { class: "chip-a o-" + a.outcome },
      h("span", {}, h("b", { text: a.responder }), h("span", { class: "chip-meta", text: `, ring ${a.ring}` })),
      h("span", { class: "chip-out" }, h("span", { "aria-hidden": "true", text: GLYPH[a.outcome] || "" }),
        ` ${OUTCOME_NAMES[a.outcome] || a.outcome}${a.reason ? ": " + words(a.reason) : ""}${a.took === null ? "" : " after " + mmss(a.took)}`))))));
  if (inc.escalations.length) {
    cols.push(h("section", { class: "wave wave-esc" }, h("h4", { text: "Coordinator" }),
      h("ul", {}, inc.escalations.map((e) => h("li", { class: "chip-a esc" },
        h("b", { text: "Escalated" }), h("span", { class: "chip-meta", text: words(e.reason) + (e.role ? ` (${e.role})` : "") }))))));
  }
  return h("div", { class: "cascade", role: "group", "aria-label": "Alert cascade" }, cols);
}

// renderIncident builds a card. ctx: open (the details opened), now (the
// server's clock), selected (the incident picked on the map) and
// onHover(id or ""). Its timeline mount is filled once the card is on the
// page and has a width (timelineChart).
export function renderIncident(inc, ctx) {
  const t1 = inc.t.T1 ? new Date(inc.t.T1) : null;
  const sev = inc.severity || "none";
  const open = !ended(inc);
  const clockEl = h("span", { class: "clock", "data-t1": t1 && open ? inc.t.T1 : null },
    t1 ? since(t1, open ? ctx.now : Math.max(...Object.values(inc.t).map((v) => new Date(v)))) : "—",
    h("small", { text: open ? "since T1" : "T1 to last" }));
  const facts = [inc.category, inc.ward, inc.landmark].filter(Boolean).join(" · ") || "Answers not given yet";
  const tags = [];
  if (inc.escalated) tags.push(h("li", { class: "esc", text: "Escalated to a coordinator" }));
  for (const f of inc.flags || []) tags.push(h("li", { text: words(f) }));
  if (inc.location) tags.push(h("li", { text: "located by " + inc.location }));
  if (inc.wake && open) tags.push(h("li", { text: "next dispatch step " + clock(new Date(inc.wake)) }));
  const ttimes = h("ol", { class: "ttimes", "aria-label": "Protocol timestamps" },
    ["T1", "T2", "T3", "T4", "T5", "T6", "T7"].map((k) => {
      const v = inc.t[k];
      return h("li", { class: v ? "done" : "" }, h("b", { text: k }),
        h("span", { class: "v", text: v && t1 ? "+" + since(t1, new Date(v)) : "—" }), " " + tNames[k]);
    }));
  const alerts = details(ctx.open, inc.id + ":alerts", "Alerts (" + inc.alerts.length + ")",
    inc.alerts.length ? table(["Responder", "Ring", "Sent", "Outcome", "Delivery"], inc.alerts.map((a) => [
      a.responder + " (" + a.role + ")", String(a.ring), clock(new Date(a.sentAt)),
      (OUTCOME_NAMES[a.outcome] || a.outcome) + (a.reason ? " — " + a.reason : "") + (a.respondedAt ? " at " + clock(new Date(a.respondedAt)) : ""),
      attempts(a.attempts)])) : h("p", { class: "empty", text: "No responder alerted yet." }));
  const kids = [alerts];
  if (inc.escalations.length) {
    kids.push(details(ctx.open, inc.id + ":esc", "Escalations (" + inc.escalations.length + ")",
      table(["Time", "Reason", "Role"], inc.escalations.map((e) => [clock(new Date(e.at)), words(e.reason), e.role || "—"]))));
  }
  if (inc.notices.length) {
    kids.push(details(ctx.open, inc.id + ":notices", "Caller notices (" + inc.notices.length + ")",
      table(["Notice", "Channel", "Status"], inc.notices.map((n) => [words(n.kind), n.channel, n.status + (n.delivery ? " (" + n.delivery + ")" : "")]))));
  }
  kids.push(details(ctx.open, inc.id + ":events", "Event timeline (" + inc.events.length + ")",
    table(["#", "Time", "Event", "Actor", "Coded payload"], inc.events.map((e) => [
      String(e.seq), clock(new Date(e.at)), e.type, e.actor, h("span", { class: "payload", text: e.payload })]))));
  return h("article", { class: "incident " + sev + (inc.id === ctx.selected ? " selected" : ""), id: "inc-" + inc.id, tabindex: "-1",
    "aria-labelledby": "ref-" + inc.id, "data-inc": inc.id,
    onmouseenter: () => ctx.onHover(inc.id), onmouseleave: () => ctx.onHover(""),
    onfocusin: () => ctx.onHover(inc.id), onfocusout: () => ctx.onHover("") },
  h("div", { class: "inc-head" },
    h("span", { class: "chip " + sev, text: sev === "none" ? "UNCLASSIFIED" : sev.toUpperCase() }),
    h("h3", { id: "ref-" + inc.id, "aria-label": "Incident " + (inc.reference || ""), text: inc.reference || "—" }),
    h("span", { class: "state", text: stateNames[inc.state] || inc.state }),
    clockEl),
  h("p", { class: "facts", text: facts }),
  tags.length ? h("ul", { class: "tags" }, tags) : null,
  h("div", { class: "tl-mount", "data-tl": inc.id }),
  ttimes, cascadeView(inc), kids);
}
