// The analytics panel: two charts of the protocol timestamps, drawn as SVG
// (the page loads no third-party script), each with a hover and focus
// tooltip, and a table of every figure. Text wears text colours; only the
// marks carry the stage colours. Every value from the server is set as text.
import { h, svg } from "./dom.js";
import { mmss } from "./format.js";
import { stageStats, incidentStages, PHASES } from "./analytics.js";

// The stages in protocol order, categorical slots 1 to 6 in fixed order
// (validated: CVD-separated on white; three sit under 3:1, so the legend,
// the tooltip and the table carry every value too).
export const STAGE_COLOURS = ["#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300"];
const MEDIAN_COLOUR = "#2a78d6";
const R = 4; // a bar's rounded data end
const GAP = 2; // the surface gap between touching segments

// bar draws a horizontal bar from x0 to x1 at y, h tall: square at the
// baseline, a 4px rounded data end when round.
function bar(x0, x1, y, hgt, round, attrs) {
  const w = Math.max(0, x1 - x0);
  const r = round ? Math.min(R, w, hgt / 2) : 0;
  const d = `M${x0.toFixed(1)} ${y}H${(x1 - r).toFixed(1)}` + (r ? `Q${x1.toFixed(1)} ${y} ${x1.toFixed(1)} ${y + r}V${y + hgt - r}Q${x1.toFixed(1)} ${y + hgt} ${(x1 - r).toFixed(1)} ${y + hgt}` : `V${y + hgt}`) + `H${x0.toFixed(1)}Z`;
  return svg("path", { d, ...attrs });
}

function axis(ticksList, x, top, bottom, label) {
  return svg("g", { class: "ch-axis", "aria-hidden": "true" },
    ticksList.map((t) => svg("line", { x1: x(t).toFixed(1), x2: x(t).toFixed(1), y1: String(top), y2: String(bottom), class: "ch-grid" })),
    ticksList.map((t, i) => svg("text", { x: x(t).toFixed(1), y: String(bottom + 14), class: "ch-tick", "text-anchor": i === 0 ? "start" : i === ticksList.length - 1 ? "end" : "middle", text: mmss(t) })),
    label ? svg("text", { x: x(ticksList[ticksList.length - 1]).toFixed(1), y: String(bottom + 28), class: "ch-tick", "text-anchor": "end", text: label }) : null);
}

// niceTicks: 0 to max in round minute:second steps (at most six).
function niceTicks(max) {
  const steps = [5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600].map((s) => s * 1000);
  const step = steps.find((s) => max / s <= 5) || Math.ceil(max / 5 / 3600000) * 3600000;
  const out = [];
  for (let t = 0; t <= max + step - 1; t += step) out.push(t);
  return out;
}

export class Analytics {
  constructor(mount) {
    this.mount = mount;
    this.tip = h("div", { class: "ch-tip", role: "status", hidden: true });
    this.medians = h("div", { class: "ch-mount" });
    this.stacks = h("div", { class: "ch-mount" });
    this.legend = h("ul", { class: "ch-legend", "aria-label": "Stages" },
      PHASES.map((p, i) => h("li", {}, svg("svg", { viewBox: "0 0 12 12", width: "12", height: "12", "aria-hidden": "true" },
        svg("rect", { width: "12", height: "12", fill: STAGE_COLOURS[i] })), p.label)));
    this.table = h("div", { class: "ch-table" });
    mount.replaceChildren(
      h("div", { class: "charts" },
        h("figure", { class: "chart" },
          h("figcaption", {}, h("h3", { text: "Median time in each stage" }),
            h("p", { class: "hint", text: "Over the incidents that completed the stage. n is how many did." })),
          this.medians),
        h("figure", { class: "chart" },
          h("figcaption", {}, h("h3", { text: "Each incident, stage by stage" }),
            h("p", { class: "hint", text: "The latest calls, from the call (T1). A faded stage is still running." })),
          this.legend, this.stacks)),
      h("details", { class: "ch-details" }, h("summary", { text: "Show the figures as a table" }), this.table),
      this.tip);
    this.key = "";
  }

  // render draws both charts from the board when it or the widths changed.
  render(board, now) {
    if (!board) return;
    const incidents = board.incidents;
    const stats = stageStats(incidents);
    const stages = incidentStages(incidents, now);
    const key = JSON.stringify([stats, stages.rows.map((r) => [r.id, r.segments.map((s) => [s.ms >> 10, s.open])]), this.medians.clientWidth, this.stacks.clientWidth]);
    if (key === this.key) return;
    this.key = key;
    this.drawMedians(stats);
    this.drawStacks(stages);
    this.drawTable(stats, stages);
  }

  show(e, lines) {
    this.tip.replaceChildren(...lines.map(([value, label, colour]) => h("p", {},
      colour ? svg("svg", { viewBox: "0 0 14 4", width: "14", height: "4", "aria-hidden": "true" }, svg("line", { x1: "0", x2: "14", y1: "2", y2: "2", stroke: colour, "stroke-width": "3", "stroke-linecap": "round" })) : null,
      h("b", { text: value }), h("span", { text: label }))));
    this.tip.hidden = false;
    const box = this.mount.getBoundingClientRect();
    const r = e.target.getBoundingClientRect ? e.target.getBoundingClientRect() : null;
    const x = e.clientX ?? (r ? r.left + r.width / 2 : box.left);
    const y = e.clientY ?? (r ? r.top : box.top);
    const left = Math.min(Math.max(8, x - box.left + 12), box.width - this.tip.offsetWidth - 8);
    this.tip.style.left = left + "px";
    this.tip.style.top = Math.max(0, y - box.top - this.tip.offsetHeight - 10) + "px";
  }

  hide() { this.tip.hidden = true; }

  hover(el, lines) {
    // The innermost mark answers: a segment's tooltip, not its row's.
    el.addEventListener("pointermove", (e) => { e.stopPropagation(); this.show(e, lines()); });
    el.addEventListener("pointerleave", () => this.hide());
    el.addEventListener("focus", (e) => this.show(e, lines()));
    el.addEventListener("blur", () => this.hide());
  }

  // drawMedians: one row per stage, its name on a line of its own above its
  // bar, so a long name never meets the bar at any width.
  drawMedians(stats) {
    const W = Math.max(260, this.medians.clientWidth);
    const rowH = 44;
    const top = 4;
    const H = top + stats.length * rowH + 22;
    const max = Math.max(1000, ...stats.map((s) => s.median || 0));
    const tk = niceTicks(max);
    const x = (ms) => (ms / tk[tk.length - 1]) * (W - 84);
    const rows = stats.map((s, i) => {
      const y = top + i * rowH;
      const g = svg("g", { class: "ch-row", tabindex: "0", role: "img",
        "aria-label": s.n ? `${s.label}: median ${mmss(s.median)} over ${s.n} incident${s.n === 1 ? "" : "s"}, fastest ${mmss(s.min)}, slowest ${mmss(s.max)}` : `${s.label}: no incident has completed it yet` },
      svg("rect", { x: "0", y: String(y), width: String(W), height: String(rowH), class: "ch-hit" }),
      svg("text", { x: "0", y: String(y + 15), class: "ch-label", text: s.label }),
      s.n ? bar(x(0), Math.max(x(0) + 2, x(s.median)), y + 22, 14, true, { fill: MEDIAN_COLOUR, class: "ch-mark" }) : null,
      s.n
        ? svg("text", { x: (Math.max(x(0) + 2, x(s.median)) + 6).toFixed(1), y: String(y + 33), class: "ch-value" }, svg("tspan", { text: mmss(s.median) }), svg("tspan", { class: "ch-n", dx: "6", text: `n ${s.n}` }))
        : svg("text", { x: "0", y: String(y + 33), class: "ch-n", text: "no data yet" }));
      if (s.n) this.hover(g, () => [[mmss(s.median), `median, ${s.label.toLowerCase()}`, MEDIAN_COLOUR], [mmss(s.min), "fastest"], [mmss(s.max), "slowest"], [String(s.n), s.n === 1 ? "incident" : "incidents"]]);
      return g;
    });
    this.medians.replaceChildren(svg("svg", { class: "ch-svg", viewBox: `0 0 ${W} ${H}`, width: "100%", height: String(H), role: "group", "aria-label": "Median time in each stage" },
      axis(tk, x, top, top + stats.length * rowH), svg("line", { x1: "0.5", x2: "0.5", y1: String(top), y2: String(top + stats.length * rowH), class: "ch-base" }), rows));
  }

  drawStacks({ rows, span, ticks }) {
    if (!rows.length) {
      this.stacks.replaceChildren(h("p", { class: "placeholder", text: "No incident has timestamps yet." }));
      return;
    }
    const W = Math.max(280, this.stacks.clientWidth);
    const labelW = 104;
    const rowH = 32;
    const top = 6;
    const H = top + rows.length * rowH + 22;
    const end = ticks[ticks.length - 1] < span ? span : ticks[ticks.length - 1];
    const x = (ms) => labelW + (ms / end) * (W - labelW - 8);
    const out = rows.map((r, i) => {
      const y = top + i * rowH;
      const last = r.segments.length - 1;
      const g = svg("g", { class: "ch-row", tabindex: "0", role: "img",
        "aria-label": `Incident ${r.ref}: ` + r.segments.map((s) => `${s.label} ${mmss(s.ms)}${s.open ? " so far" : ""}`).join(", ") },
      svg("rect", { x: "0", y: String(y), width: String(W), height: String(rowH), class: "ch-hit" }),
      svg("text", { x: "0", y: String(y + 20), class: "ch-label num", text: r.ref }),
      r.segments.map((s, k) => {
        const x0 = x(s.start);
        const x1 = Math.max(x0 + 1, x(s.start + s.ms) - (k < last ? GAP : 0));
        const seg = bar(x0, x1, y + 7, 18, k === last, { fill: STAGE_COLOURS[s.slot], class: "ch-mark" + (s.open ? " open" : "") });
        this.hover(seg, () => [[mmss(s.ms) + (s.open ? " so far" : ""), s.label, STAGE_COLOURS[s.slot]], [r.ref, "incident"]]);
        return seg;
      }));
      this.hover(g, () => r.segments.map((s) => [mmss(s.ms) + (s.open ? " so far" : ""), s.label, STAGE_COLOURS[s.slot]]));
      return g;
    });
    this.stacks.replaceChildren(svg("svg", { class: "ch-svg", viewBox: `0 0 ${W} ${H}`, width: "100%", height: String(H), role: "group", "aria-label": "Each incident, stage by stage" },
      axis(ticks, x, top, top + rows.length * rowH, "since the call"), out));
  }

  drawTable(stats, { rows }) {
    const head = ["", ...PHASES.map((p) => p.label)];
    this.table.replaceChildren(
      h("table", {}, h("thead", {}, h("tr", {}, head.map((c) => h("th", { scope: "col", text: c })))),
        h("tbody", {},
          h("tr", {}, h("th", { scope: "row", text: "Median" }), stats.map((s) => h("td", { class: "num", text: s.n ? `${mmss(s.median)} (n ${s.n})` : "—" }))),
          rows.map((r) => h("tr", {}, h("th", { scope: "row", class: "num", text: r.ref }),
            PHASES.map((_, slot) => { const s = r.segments.find((x) => x.slot === slot); return h("td", { class: "num", text: s ? mmss(s.ms) + (s.open ? " so far" : "") : "—" }); }))))));
  }
}
