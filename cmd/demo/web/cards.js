// The live queue: one compact row per incident, most urgent first, and the
// selected incident in full beside it: its facts, the response timeline, the
// alert waves and the tables. Readings are plain text in a text-safe colour.
import { h, svg, clock } from "./dom.js";
import { ended, mmss, words } from "./format.js";
import { timeline, cascade } from "./metrics.js";
import { OUTCOME_NAMES } from "./views.js";
import { groupOf, overdue, roleCell, counts, filtered, GROUP_NAMES } from "./queue.js";

const tNames = { T1: "call", T2: "ready to dispatch", T3: "first alert", T4: "accepted", T5: "arrived", T6: "departed", T7: "at facility" };
const stateNames = { intake: "Taking the call", triaged: "Triaged", dispatching: "Finding help", assigned: "Help assigned",
  on_scene: "On scene", transporting: "Transporting", at_facility: "At facility", closed: "Closed", merged: "Merged" };
const GLYPH = { pending: "…", accepted: "✓", declined: "✕", no_response: "○", stood_down: "↓" };
const TONE = { pending: "pending", accepted: "accepted", declined: "declined", no_response: "declined", stood_down: "none" };
const STATE_TONE = { waiting: "pending", assigned: "accepted", escalated: "declined", done: "none" };
export const SEV_WORD = { red: "Red", yellow: "Yellow", green: "Green", none: "Unclassified" };

export const since = (from, to) => mmss(to - from);

function details(open, key, summary, ...body) {
  const d = h("details", { open: open.has(key), "data-key": key, ontoggle: () => (d.open ? open.add(key) : open.delete(key)) },
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

// cascadeView lists the alert waves, one row per alert with its outcome in
// colour, and the escalations to a coordinator last.
function cascadeView(inc) {
  const waves = cascade(inc);
  if (!waves.length && !inc.escalations.length) return null;
  const cols = waves.map((w) => h("section", { class: "wave" }, h("h4", { text: "Wave " + w.wave }),
    h("ul", {}, w.alerts.map((a) => h("li", { class: "alert-row" },
      h("span", {}, h("b", { text: a.responder }), h("span", { class: "alert-meta", text: `, ring ${a.ring}` })),
      h("span", { class: "alert-out tone-" + (TONE[a.outcome] || "none") }, h("span", { "aria-hidden": "true", text: GLYPH[a.outcome] || "" }),
        ` ${OUTCOME_NAMES[a.outcome] || a.outcome}${a.reason ? ": " + words(a.reason) : ""}${a.took === null ? "" : " after " + mmss(a.took)}`))))));
  if (inc.escalations.length) {
    cols.push(h("section", { class: "wave wave-esc" }, h("h4", { text: "Coordinator" }),
      h("ul", {}, inc.escalations.map((e) => h("li", { class: "alert-row" },
        h("b", { class: "tone-declined", text: "Escalated" }), h("span", { class: "alert-meta", text: words(e.reason) + (e.role ? ` (${e.role})` : "") }))))));
  }
  return h("div", { class: "cascade", role: "group", "aria-label": "Alert waves" }, cols);
}

// renderIncident builds the selected incident's detail. ctx: open (the
// details opened), now (the server's clock), onHover(id or ""), onClose(id)
// and closing (the close under way or its answer). Its timeline mount is
// filled once it is on the page and has a width (timelineChart).
export function renderIncident(inc, ctx) {
  const t1 = inc.t.T1 ? new Date(inc.t.T1) : null;
  const sev = inc.severity || "none";
  const open = !ended(inc);
  const clockEl = h("span", { class: "clock", "data-t1": t1 && open ? inc.t.T1 : null },
    t1 ? since(t1, open ? ctx.now : Math.max(...Object.values(inc.t).map((v) => new Date(v)))) : "—",
    h("small", { text: open ? "since T1" : "T1 to last" }));
  const facts = [inc.category, inc.ward, inc.landmark].filter(Boolean).join(" · ") || "Answers not given yet";
  const tags = [];
  if (inc.mergedInto) tags.push(h("li", { text: "Merged into another call" }));
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
  if (ctx.arrival) kids.push(arrivalView(inc, ctx.arrival, ctx));
  kids.push(details(ctx.open, inc.id + ":events", "Event timeline (" + inc.events.length + ")",
    table(["#", "Time", "Event", "Actor", "Coded payload"], inc.events.map((e) => [
      String(e.seq), clock(new Date(e.at)), e.type, e.actor, h("span", { class: "payload", text: e.payload })]))));
  const closable = !ended(inc);
  const closing = ctx.closing && ctx.closing.id === inc.id ? ctx.closing : null;
  const actions = closable ? h("div", { class: "detail-actions" },
    h("button", { type: "button", class: "btn btn-quiet btn-small", disabled: Boolean(closing && closing.busy), onclick: () => ctx.onClose(inc.id),
      text: "Close incident" }),
    h("p", { class: closing && closing.bad ? "bad" : "", role: "status",
      text: closing && closing.text ? closing.text : "Alerts still waiting are stood down and the responders freed. A stand-in for the M5 console." })) : null;
  return h("article", { class: "incident", id: "inc-" + inc.id, tabindex: "-1",
    "aria-labelledby": "ref-" + inc.id, "data-inc": inc.id,
    onmouseenter: () => ctx.onHover(inc.id), onmouseleave: () => ctx.onHover(""),
    onfocusin: () => ctx.onHover(inc.id), onfocusout: () => ctx.onHover("") },
  h("div", { class: "inc-head" },
    h("span", { class: "sev " + sev }, h("span", { class: "sev-dot " + sev, "aria-hidden": "true" }), SEV_WORD[sev]),
    h("h3", { id: "ref-" + inc.id, "aria-label": "Incident " + (inc.reference || ""), text: inc.reference || "—" }),
    h("span", { class: "state tone-" + STATE_TONE[groupOf(inc)], text: stateNames[inc.state] || inc.state }),
    clockEl),
  h("p", { class: "facts", text: facts }),
  tags.length ? h("ul", { class: "tags" }, tags) : null,
  actions,
  h("div", { class: "tl-mount", "data-tl": inc.id }),
  ttimes, cascadeView(inc), kids);
}

const ANSWER = { yes: "Yes, help arrived", no: "No, still waiting", none: "No answer" };

// arrivalView is the arrival check of an incident: each time the caller was
// asked whether help has arrived, their answer and what Sauti did, and, once
// they said yes, the window the arrival falls in (a demo preview, arrival.go).
function arrivalView(inc, a, ctx) {
  const who = ctx.label ? ctx.label(a.phone) : a.phone;
  const every = a.everySec >= 60 ? `${a.everySec / 60} min` : `${a.everySec} s`;
  let lead;
  if (a.confirmedAt) {
    const t5 = inc.t.T5 ? ` The responder recorded the arrival (T5) at ${clock(new Date(inc.t.T5))}.` : "";
    lead = h("p", { class: "arrival-lead ok" }, h("b", { text: `Help arrived between ${clock(new Date(a.after))} and ${clock(new Date(a.confirmedAt))}` }),
      `, by ${who}'s answers.${t5}`);
  } else if (a.open) {
    lead = h("p", { class: "arrival-lead", text: `Asked ${who} at ${clock(new Date(a.open.at))}: waiting for the answer.` });
  } else if (a.next) {
    lead = h("p", { class: "arrival-lead", text: `${who} is asked every ${every} until they say yes. Next question at ${clock(new Date(a.next))}.` });
  } else {
    lead = h("p", { class: "arrival-lead", text: "The incident ended before the caller said help arrived." });
  }
  const asks = a.asks || [];
  return details(ctx.open, inc.id + ":arrival", `Has help arrived? (${asks.length} asked)`,
    lead,
    asks.length ? table(["Asked", "Answer", "Answered", "What Sauti did"], asks.map((q) => [
      clock(new Date(q.at)), q.answer ? ANSWER[q.answer] + (q.via ? ` (${q.via === "ussd" ? "USSD" : "app"})` : "") : "Waiting",
      q.answeredAt ? clock(new Date(q.answeredAt)) : "—", q.did || "—"])) : null,
    h("p", { class: "empty", text: "A demo preview of the caller's arrival check (Build Prompt §10.4): the answers stay in the demo." }));
}

// waitClock: the time since the call, live while open, as the detail's.
function waitClock(inc, now) {
  const t1 = inc.t.T1 ? Date.parse(inc.t.T1) : null;
  const open = groupOf(inc) !== "done";
  const last = Math.max(...Object.values(inc.t).map((v) => Date.parse(v)));
  const late = overdue(inc, now);
  return h("span", { class: "q-wait clock-q" + (late ? " overdue" : ""), "data-t1": t1 && !ended(inc) ? inc.t.T1 : null },
    t1 ? mmss((ended(inc) ? last : now) - t1) : "—",
    h("small", { text: late ? "no one yet" : open ? "since call" : "call to end" }));
}

function roleText(inc, role, name) {
  const c = roleCell(inc, role);
  const what = c.who ? `${c.who} ${c.text}` : c.text;
  return h("span", { class: "q-role" }, h("b", { text: name }), h("span", { class: "tone-" + c.tone, text: what + (c.ms !== null ? " in " + mmss(c.ms) : "") }));
}

// renderQueue draws the filter buttons and the rows, and returns the rows
// shown. ctx: filter, selected, now, empty (the text for no incidents),
// onFilter(group), onSelect(id), onHover(id or "").
export function renderQueue(filters, list, incidents, ctx) {
  const n = counts(incidents);
  filters.replaceChildren(...["all", "waiting", "assigned", "escalated", "done"].map((g) => h("button", { type: "button", class: "filter",
    "aria-pressed": String(ctx.filter === g), onclick: () => ctx.onFilter(g) }, GROUP_NAMES[g], h("span", { text: String(n[g]) }))));
  const rows = filtered(incidents, ctx.filter);
  if (!incidents.length) {
    list.replaceChildren(h("p", { class: "placeholder", text: ctx.empty }));
    return rows;
  }
  if (!rows.length) {
    list.replaceChildren(h("p", { class: "placeholder", text: `No ${GROUP_NAMES[ctx.filter].toLowerCase()} incidents.` }));
    return rows;
  }
  list.replaceChildren(h("div", { class: "queue-head", "aria-hidden": "true" }, h("span", { text: "Priority" }), h("span", { text: "Incident" }),
    h("span", { text: "Responders" }), h("span", { class: "q-wait", text: "Wait" })),
  ...rows.map((inc) => {
    const sev = inc.severity || "none";
    const group = groupOf(inc);
    const where = [inc.landmark && inc.landmark !== "other" ? inc.landmark : "", inc.ward].filter(Boolean).join(", ");
    return h("button", { type: "button", class: "qrow" + (group === "done" ? " done" : ""), "aria-pressed": String(inc.id === ctx.selected), "data-inc": inc.id,
      "aria-label": `${SEV_WORD[sev]} incident ${inc.reference}, ${stateNames[inc.state] || inc.state}`,
      onclick: () => ctx.onSelect(inc.id), onmouseenter: () => ctx.onHover(inc.id), onmouseleave: () => ctx.onHover(""),
      onfocus: () => ctx.onHover(inc.id), onblur: () => ctx.onHover("") },
    h("span", { class: "q-sevcell sev " + sev }, h("span", { class: "sev-dot " + sev, "aria-hidden": "true" }), SEV_WORD[sev]),
    h("span", { class: "q-main" }, h("span", { class: "q-ref", text: inc.reference || "—" }),
      h("span", { class: "q-sub", text: [inc.category, where].filter(Boolean).join(" · ") || "Answers not given yet" })),
    h("span", { class: "q-who" }, h("span", { class: "q-state tone-" + STATE_TONE[group], text: stateNames[inc.state] || inc.state }),
      roleText(inc, "vht", "VHT"), roleText(inc, "transporter", "Transport")),
    waitClock(inc, ctx.now));
  }));
  return rows;
}
