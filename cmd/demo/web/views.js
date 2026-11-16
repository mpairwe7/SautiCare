// The live strip, the map and the activity feed: render functions of what
// metrics.js, geo.js and feed.js derive from the snapshot.
import { h, svg, clock } from "./dom.js";
import { mmss } from "./format.js";
import { summary } from "./geo.js";

const SEVERITY_NAMES = { red: "red", yellow: "yellow", green: "green", none: "not triaged" };
export const OUTCOME_NAMES = { pending: "waiting", accepted: "accepted", declined: "declined", no_response: "no response", stood_down: "stood down" };

// segbar is a thin bar of one whole split into parts; the list beside it
// carries the figures, so the bar holds no text.
function segbar(parts, label) {
  const total = parts.reduce((n, p) => n + p.n, 0);
  const bar = h("div", { class: "segbar", role: "img", "aria-label": label });
  for (const p of parts) {
    if (!p.n) continue;
    const seg = h("span", { class: "seg " + p.cls });
    seg.style.flexGrow = String(p.n / total);
    bar.append(seg);
  }
  return [total ? bar : h("div", { class: "segbar empty", "aria-hidden": "true" }),
    h("ul", { class: "parts" }, parts.map((p) => h("li", {}, h("i", { class: "key " + p.cls, "aria-hidden": "true" }), `${p.n} ${p.name}`)))];
}

function tile(label, value, ...rest) {
  return h("article", { class: "kpi" }, h("h3", { text: label }), h("p", { class: "kpi-value", text: value }), rest);
}

export function renderKpis(mount, m) {
  const sev = Object.entries(m.active).map(([k, n]) => ({ n, name: SEVERITY_NAMES[k], cls: "sev-" + k }));
  const out = Object.entries(m.outcomes).map(([k, n]) => ({ n, name: OUTCOME_NAMES[k] || k, cls: "o-" + k }));
  const words = (parts) => parts.map((p) => `${p.n} ${p.name}`).join(", ");
  mount.replaceChildren(
    tile("Open incidents", String(m.activeTotal), segbar(sev, "Open incidents by severity: " + words(sev))),
    tile("Call to acceptance", m.acceptance.median === null ? "—" : mmss(m.acceptance.median),
      h("p", { class: "kpi-note", text: m.acceptance.count ? `Median T1 to T4 over ${m.acceptance.count} accepted incident${m.acceptance.count === 1 ? "" : "s"}` : "No incident accepted yet" })),
    tile("Alerts", String(m.alerts), segbar(out, "Alerts by outcome: " + words(out))),
    tile("Escalations", String(m.escalations.count),
      h("p", { class: "kpi-note", text: m.escalations.count ? `On ${m.escalations.incidents} incident${m.escalations.incidents === 1 ? "" : "s"}, each to a coordinator` : "None so far" })),
    tile("SMS sent", String(m.sms.count), h("p", { class: "kpi-note", text: `To ${m.sms.phones} of the phones on screen` })),
  );
}

const LETTER = { red: "R", yellow: "Y", green: "G", none: "?" };

// renderMap draws the layout. Selecting a pin (click, Enter or Space) calls
// onSelect with its incident; keyboard focus survives a redraw.
export function renderMap(mount, lay, selected, onSelect) {
  const focused = mount.contains(document.activeElement) ? document.activeElement.dataset.inc : "";
  const pins = lay.incidents.map((p) => {
    const name = `Incident ${p.ref}, ${SEVERITY_NAMES[p.severity]}, ${p.merged ? "merged" : p.active ? "open" : "closed"}, at ${p.place}`;
    const g = svg("g", { class: `pin sev-${p.severity}${p.active ? " active" : ""}${p.merged ? " merged" : ""}${p.id === selected ? " hl" : ""}`,
      transform: `translate(${p.x.toFixed(1)} ${p.y.toFixed(1)})`, tabindex: "0", role: "button", "aria-label": name, "data-inc": p.id,
      onclick: () => onSelect(p.id),
      onkeydown: (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onSelect(p.id); } } },
    svg("title", { text: name }),
    p.active ? svg("circle", { class: "pulse", r: "8" }) : null,
    svg("circle", { class: "ring", r: "12" }),
    svg("circle", { class: "dot", r: "8" }),
    svg("text", { class: "letter", y: "3.5", text: LETTER[p.severity] }),
    svg("text", { class: "pin-label", x: "11", y: "-8", text: p.ref.slice(-4) }));
    return g;
  });
  const el = svg("svg", { class: "map-svg", viewBox: `0 0 ${lay.width} ${lay.height}`, role: "group", "aria-labelledby": "map-h", "aria-describedby": "map-summary" },
    svg("g", {}, lay.wards.map((w) => [
      svg("rect", { class: "ward", x: w.x, y: w.y, width: w.w, height: w.h, rx: "12" }),
      svg("text", { class: "ward-name", x: w.x + 8, y: w.y + 14, text: w.name }),
    ])),
    svg("g", {}, lay.landmarks.map((l) => svg("circle", { class: "landmark", cx: l.x, cy: l.y, r: "2.5" }, svg("title", { text: l.name })))),
    svg("g", {}, lay.links.map((l) => svg("line", { class: `link o-${l.outcome}${l.incident === selected ? " hl" : ""}`, "data-inc": l.incident,
      x1: l.from.x, y1: l.from.y, x2: l.to.x, y2: l.to.y }))),
    svg("g", {}, lay.responders.map((r) => svg("g", { class: "resp", transform: `translate(${r.x.toFixed(1)} ${r.y.toFixed(1)})` },
      svg("title", { text: `${r.label}, ${r.ward}` }),
      svg("rect", { x: "-8", y: "-8", width: "16", height: "16", rx: "3" }),
      svg("text", { y: "3.5", text: r.short })))),
    svg("g", {}, pins));
  mount.replaceChildren(el);
  document.getElementById("map-summary").textContent = summary(lay);
  if (focused) el.querySelector(`[data-inc="${CSS.escape(focused)}"]`)?.focus();
}

// highlight marks one incident's pin and lines, or none, without a redraw.
export function highlight(mount, id) {
  for (const el of mount.querySelectorAll("[data-inc]")) el.classList.toggle("hl", el.dataset.inc === id);
}

const FEED_MAX = 150;

// renderFeed lists the newest items; one not seen before slides in.
export function renderFeed(mount, items, seen) {
  const first = seen.size === 0;
  mount.replaceChildren(...items.slice(0, FEED_MAX).map((it) => {
    const fresh = !first && !seen.has(it.key);
    seen.add(it.key);
    const relay = it.relayedAt
      ? h("p", { class: "relay" }, h("span", { class: "svc svc-outbox-relay", text: "outbox-relay" }),
        ` sealed it in the audit ledger after ${((Date.parse(it.relayedAt) - Date.parse(it.at)) / 1000).toFixed(1)} s`)
      : null;
    return h("li", { class: "item" + (fresh ? " new" : "") },
      h("div", { class: "item-head" },
        h("time", { datetime: it.at, text: clock(new Date(it.at)) }),
        h("span", { class: "svc svc-" + it.service.toLowerCase(), text: it.service }),
        it.ref ? h("span", { class: "item-ref", text: it.ref }) : null),
      h("p", { class: "item-text", text: it.text }), relay);
  }));
  if (!items.length) mount.replaceChildren(h("li", { class: "empty", text: "Nothing yet. Dial a phone or play a scenario." }));
}
