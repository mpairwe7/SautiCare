// Sauti Dispatch demo UI: the composition root. It polls one snapshot
// ({state, board, sms}) and renders every view from it; every simulated
// action goes through the same USSD API as the on-screen phones. Every
// value from the server is set as text, never as HTML. The server only ever
// sends masked phone numbers.
import { h, svg } from "./dom.js";
import { mmss, REF } from "./format.js";
import { metrics, boardSms } from "./metrics.js";
import { feedItems } from "./feed.js";
import { layout, located, homeFix } from "./geo.js";
import { renderKpis, renderMap, renderFeed, highlight } from "./views.js";
import { renderIncident, renderQueue, timelineChart } from "./cards.js";
import { SCENARIOS, runScenario, phonesOf, answerAlert, lockPhones, lockedBy, IDLE } from "./scenarios.js";
import { AUTOPILOT, plan } from "./autopilot.js";
import { BasicPhone, sleep } from "./phone.js";
import { SmartPhone } from "./smart.js";
import { responderStatus } from "./responders.js";
import { Analytics } from "./charts.js";

const $ = (sel) => document.querySelector(sel);
const reducedMotion = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

async function api(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) {
    opt.headers["Content-Type"] = "application/json";
    opt.body = JSON.stringify(body);
  }
  try {
    const res = await fetch(path, opt);
    let data = {};
    try { data = await res.json(); } catch { /* not JSON */ }
    return { status: res.status, data };
  } catch {
    return { status: 0, data: { error: "The demo cannot be reached." } };
  }
}

// Per-viewer conveniences, kept in this browser only: which phone type each
// panel shows. Storage may be unavailable; the page works without it.
const store = {
  get(k, d) { try { return localStorage.getItem("sauti-demo:" + k) || d; } catch { return d; } },
  set(k, v) { try { localStorage.setItem("sauti-demo:" + k, v); } catch { /* not kept */ } },
};

const KINDS = ["caller", "vht", "transporter"];

const app = {
  phase: "", // the last phase the server reported; a failed poll leaves it as it was
  run: 0,
  lost: false, // the last state poll failed
  verifying: false, // an audit verification is in flight
  phones: [],
  codes: { caller: "*384*1#", responder: "*384*2#", incident: "*384*3#" },
  map: null, // the gazetteer, from /api/state
  timeoutMs: 90000, // an alert's answer window, from /api/state
  board: null,
  inbox: {},
  seen: {},
  cleared: {}, // each phone's inbox length when it was last cleared on this page
  shareTried: new Map(), // phone|incident → when this page last sent a smartphone VHT's share
  selected: { caller: "caller-a", vht: "", transporter: "" },
  modes: Object.fromEntries(KINDS.map((k) => [k, store.get("mode-" + k, "basic") === "smart" ? "smart" : "basic"])),
  gps: {}, // each smartphone's simulated GPS fix, once moved
  reports: {}, // each caller SIM's last incident reference
  open: new Set(),
  skew: 0, // server clock minus browser clock, ms
  snapKey: "",
  gen: 0, // bumped by clearRun: a response to a request from an earlier run is dropped
  incident: "", // the incident selected in the queue or on the map
  hover: "", // the incident under the pointer or focus
  filter: "all",
  closing: null, // {id, busy, text, bad}: the coordinator's close under way or its answer
  feedSeen: new Set(),
  locks: new Set(), // the locks ({why, phones}) of the scenario and the autopilot dialling phones
  scenario: IDLE,
};

const serverNow = () => Date.now() + app.skew;
const kindOf = (p) => (p.kind === "caller" ? "caller" : p.role);

// fixOf is a responder's smartphone location: where it was moved to, else
// where its simulated GPS starts, by their registered stage.
const fixOf = (p) => app.gps[p.id] || homeFix(p, app.phones, app.map);

// ---------- Phones ----------

// ctx is what a phone widget may read and do.
const ctx = {
  api,
  live: () => live(),
  codes: () => app.codes,
  locked: (id) => lockedBy(app.locks, id),
  inbox: (id) => app.inbox[id] || [],
  seen: (id, n) => { app.seen[id] = n; },
  cleared: (id) => app.cleared[id] || 0,
  clear: (id) => { app.cleared[id] = (app.inbox[id] || []).length; },
  phones: () => app.phones,
  board: () => app.board,
  map: () => app.map,
  now: () => serverNow(),
  timeoutMs: () => app.timeoutMs,
  ussd: (phone, body, keys) => drive(phone, body, keys, undefined, true),
  progress: (body) => api("POST", "api/progress", body),
  share: (body) => api("POST", "api/share", body),
  gps: { get: (id) => app.gps[id], set: (id, g) => { app.gps[id] = g; if (app.board && app.map) drawMap(); } },
  report: { get: (id) => app.reports[id], set: (id, ref) => { app.reports[id] = ref; } },
  ended: (p, text) => {
    const ref = (REF.exec(text || "") || [""])[0];
    if (p && p.kind === "caller" && ref) app.reports[p.id] = ref;
  },
};

const widgets = {};
const mountOf = (kind) => $(`#${kind}-phone`);

function makeWidget(kind) {
  const W = app.modes[kind] === "smart" ? SmartPhone : BasicPhone;
  widgets[kind] = new W(mountOf(kind), kind, ctx);
  const p = app.phones.find((x) => x.id === app.selected[kind]);
  if (p) widgets[kind].setPhone(p);
}

const HINTS = {
  caller: {
    basic: () => `Dial ${app.codes.caller} and answer each screen. Any basic phone can do this: USSD needs no data and no airtime.`,
    smart: () => "The Sauti app asks the same questions as USSD, through the same service, and fills in the ward and place from the phone's location.",
  },
  vht: {
    basic: () => `Village Health Team members get alerts by SMS. Dial ${app.codes.responder} to accept or decline, then ${app.codes.incident} to send the transporter where you are and to record your arrival.`,
    smart: () => "Alerts arrive as notifications. Accepting sends the transporter your location, facility and number. See the way to the patient and record the arrival.",
  },
  transporter: {
    basic: () => `Transporters get alerts by SMS. Dial ${app.codes.responder} to accept or decline, then ${app.codes.incident} to record the trip. The VHT's place and number come by SMS.`,
    smart: () => "Accept, see where the VHT and the patient are, then record the pickup and the arrival at the facility.",
  },
};

const ICON = {
  basic: () => svg("svg", { viewBox: "0 0 14 14", "aria-hidden": "true" }, svg("rect", { x: "3", y: "0.75", width: "8", height: "12.5", rx: "1.5", fill: "none", stroke: "currentColor", "stroke-width": "1.5" }),
    svg("rect", { x: "5", y: "3", width: "4", height: "2.5", fill: "currentColor" }), [0, 1, 2].map((r) => svg("rect", { x: "5", y: String(7 + r * 1.8), width: "4", height: "0.9", fill: "currentColor" }))),
  smart: () => svg("svg", { viewBox: "0 0 14 14", "aria-hidden": "true" }, svg("rect", { x: "3", y: "0.75", width: "8", height: "12.5", rx: "2", fill: "none", stroke: "currentColor", "stroke-width": "1.5" }),
    svg("rect", { x: "4.6", y: "2.6", width: "4.8", height: "7.6", rx: "0.5", fill: "currentColor" })),
};

function buildModes() {
  for (const kind of KINDS) {
    const group = document.querySelector(`[data-mode-for="${kind}"]`);
    group.replaceChildren(...["basic", "smart"].map((m) => h("button", { type: "button", "data-mode": m, "aria-pressed": String(app.modes[kind] === m),
      onclick: () => setMode(kind, m) }, ICON[m](), m === "basic" ? "Basic phone" : "Smartphone")));
  }
}

function setMode(kind, mode) {
  if (app.modes[kind] === mode) return;
  if (app.phones.some((p) => kindOf(p) === kind && lockedBy(app.locks, p.id))) {
    window.alert("A scenario or the autopilot is using this phone. Switch when it has finished.");
    return;
  }
  app.modes[kind] = mode;
  store.set("mode-" + kind, mode);
  for (const b of document.querySelectorAll(`[data-mode-for="${kind}"] button`)) b.setAttribute("aria-pressed", String(b.dataset.mode === mode));
  makeWidget(kind);
  renderHints();
  if (app.board && app.map) drawMap(); // smartphones show where they are; basic phones do not
}

function renderHints() {
  for (const kind of KINDS) document.querySelector(`[data-hint="${kind}"]`).textContent = HINTS[kind][app.modes[kind]]();
}

// ---------- Tabs ----------

// buildTabs builds a phone selector once per phone list; refreshTabs keeps
// its selection and live state current without replacing the buttons, so
// keyboard focus stays put.
function buildTabs(mount, phones, kind) {
  mount.replaceChildren(...phones.map((p) => h("button", { type: "button", role: "tab", class: "tab", id: "tab-" + p.id,
    "data-id": p.id, onclick: () => select(kind, p.id) }, p.label, h("span", { class: "tab-state" }))));
  mount.onkeydown = (e) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    const i = phones.findIndex((p) => p.id === app.selected[kind]);
    const next = phones[(i + (e.key === "ArrowRight" ? 1 : phones.length - 1)) % phones.length];
    select(kind, next.id);
    document.getElementById("tab-" + next.id).focus();
  };
}

// tabState is what a person's tab says about them: a live alert and its
// countdown, an open job, or unread SMS.
function tabState(p, sel) {
  if (p.kind === "responder" && app.board) {
    const st = responderStatus(app.board, p.label, app.timeoutMs);
    if (st.kind === "busy") return { cls: "busy", text: "On a job" };
    if (st.kind === "alert" && st.late) return { cls: "late", text: "Open alert" };
    if (st.kind === "alert") return { cls: "alert", text: "Alert " + mmss(Math.max(0, st.deadline - serverNow())) };
  }
  const unread = sel ? 0 : Math.max(0, (app.inbox[p.id] || []).length - (app.seen[p.id] || 0));
  return unread ? { cls: "unread", text: unread + " new" } : { cls: "", text: "" };
}

function refreshTabs() {
  for (const tab of document.querySelectorAll(".tab")) {
    const p = app.phones.find((x) => x.id === tab.dataset.id);
    if (!p) continue;
    const sel = app.selected[kindOf(p)] === p.id;
    tab.setAttribute("aria-selected", String(sel));
    tab.tabIndex = sel ? 0 : -1;
    tab.classList.toggle("driven", Boolean(lockedBy(app.locks, p.id)));
    const s = tabState(p, sel);
    const out = tab.querySelector(".tab-state");
    if (out.textContent !== s.text) out.textContent = s.text;
    out.className = "tab-state " + s.cls;
  }
}

function select(kind, id) {
  app.selected[kind] = id;
  widgets[kind].setPhone(app.phones.find((p) => p.id === id));
  refreshTabs();
}

function buildAllTabs() {
  for (const kind of KINDS) buildTabs($(`#${kind}-tabs`), app.phones.filter((p) => kindOf(p) === kind), kind);
  refreshTabs();
}

function renderPhones() {
  for (const kind of KINDS) widgets[kind].render(true);
  refreshTabs();
}

// drive sends one USSD request for a scenario, the autopilot or a smartphone
// action, showing it on that phone's widget: the widget switches to the
// phone, the key appears (typed, or tapped), then the answer. The autopilot
// leaves a widget alone while a person is part way through a session on
// another phone; it then sends the request without showing it.
async function drive(phoneId, body, keys, signal, takeOver) {
  const p = app.phones.find((x) => x.id === phoneId);
  if (!p) return { status: 0, data: { error: `${phoneId} is not on screen.` } };
  const kind = kindOf(p);
  const w = widgets[kind];
  const person = w.p && w.p.id !== phoneId && w.mode === "con" && !lockedBy(app.locks, w.p.id);
  const shown = takeOver || !person;
  if (shown && w.p?.id !== phoneId) select(kind, phoneId);
  const gen = w.gen; // a reset or another phone bumps it, even if the widget is back on this phone
  if (shown) await w.type(keys, signal);
  const res = await api("POST", "api/ussd", body);
  if (shown && gen === w.gen && widgets[kind] === w) w.show(res.status, res.data);
  return res;
}

// autoShare is what a VHT's smartphone does once they accept: it sends the
// transporter their location (its GPS), the facility they registered with
// and their number, without a question. A move while on the job sends it
// again (smart.js). A failed send is tried again after a few seconds.
function autoShare() {
  if (app.modes.vht !== "smart" || !app.board || !app.map) return;
  for (const p of app.phones.filter((x) => x.role === "vht")) {
    const st = responderStatus(app.board, p.label, app.timeoutMs);
    if (st.kind !== "busy") continue;
    if ((app.board.shares || []).some((s) => s.incident === st.inc.id && s.vht === p.label)) continue;
    const key = p.id + "|" + st.inc.id;
    if (serverNow() - (app.shareTried.get(key) || 0) < 6000) continue;
    app.shareTried.set(key, serverNow());
    const g = fixOf(p);
    if (g) api("POST", "api/share", { phone: p.id, incident: st.inc.id, facility: p.facility, lat: g.lat, lon: g.lon });
  }
}

// ---------- The live queue ----------

function onSelectIncident(id, from) {
  app.incident = id;
  const inc = app.board?.incidents.find((i) => i.id === id);
  if (inc && app.filter !== "all" && !document.querySelector(`.qrow[data-inc="${CSS.escape(id)}"]`)) app.filter = "all";
  highlight($("#map"), id);
  renderQueueView();
  if (from === "map") {
    const target = window.matchMedia("(max-width: 1100px)").matches ? $("#queue-detail") : document.querySelector(`.qrow[data-inc="${CSS.escape(id)}"]`);
    target?.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "center" });
    document.querySelector(`.qrow[data-inc="${CSS.escape(id)}"]`)?.focus({ preventScroll: true });
  }
}

function onHoverIncident(id) {
  app.hover = id;
  highlight($("#map"), id || app.incident);
}

async function closeIncident(id) {
  if (app.closing?.busy) return;
  if (!window.confirm("Close this incident as completed? Alerts still waiting are stood down, and its responders become free for the next call.")) return;
  const gen = app.gen;
  app.closing = { id, busy: true, text: "Closing…", bad: false };
  renderQueueView();
  const { status, data } = await api("POST", "api/progress", { incident: id, step: "close" });
  if (gen !== app.gen) return;
  app.closing = { id, busy: false, text: status === 200 ? "Closed. Its responders can be alerted again." : data.error || "The incident could not be closed.", bad: status !== 200 };
  renderQueueView();
}

function renderQueueView() {
  if (!app.board) return;
  const b = app.board;
  $("#suspended").hidden = !b.suspended;
  const rows = renderQueue($("#queue-filters"), $("#queue"), b.incidents, {
    filter: app.filter, selected: app.incident, now: serverNow(),
    empty: "No incidents yet. Report one from the caller phone, or play a scenario.",
    onFilter: (g) => { app.filter = g; renderQueueView(); },
    onSelect: (id) => onSelectIncident(id, "queue"),
    onHover: onHoverIncident,
  });
  if (!rows.some((i) => i.id === app.incident)) app.incident = rows.length ? rows[0].id : "";
  for (const r of document.querySelectorAll(".qrow")) r.setAttribute("aria-pressed", String(r.dataset.inc === app.incident));
  renderDetail();
}

function renderDetail() {
  const pane = $("#queue-detail");
  const inc = app.board?.incidents.find((i) => i.id === app.incident);
  if (!inc) {
    pane.replaceChildren(h("p", { class: "placeholder", text: "Select an incident to see its timeline, alerts and events." }));
    return;
  }
  // Rebuilding drops keyboard focus: it goes back to the same summary, by its key.
  const active = pane.contains(document.activeElement) ? document.activeElement : null;
  const key = active?.closest("details")?.dataset.key;
  pane.replaceChildren(renderIncident(inc, { open: app.open, now: serverNow(), onHover: onHoverIncident, onClose: closeIncident, closing: app.closing }));
  renderTimelines(true);
  if (key) pane.querySelector(`details[data-key="${CSS.escape(key)}"] > summary`)?.focus({ preventScroll: true });
}

// renderTimelines redraws the timeline chart at its mount's width: after a
// change, then each second while the incident is open.
function renderTimelines(all) {
  if (!app.board) return;
  const byId = Object.fromEntries(app.board.incidents.map((i) => [i.id, i]));
  for (const mount of document.querySelectorAll(".tl-mount")) {
    const inc = byId[mount.dataset.tl];
    if (!inc || (!all && (inc.state === "closed" || inc.state === "merged"))) continue;
    mount.replaceChildren(timelineChart(inc, serverNow(), mount.clientWidth) || "");
  }
}

// ---------- Snapshot views ----------

// drawMap lays the map out at its mount's size, one SVG unit to the pixel.
function drawMap() {
  const mount = $("#map");
  const where = located(app.phones, app.modes, fixOf, app.board);
  renderMap(mount, layout(app.map, app.phones, app.board, { width: mount.clientWidth, height: mount.clientHeight, where }), app.hover || app.incident,
    (id) => onSelectIncident(id, "map"));
}

// renderSnapshot renders every view of the snapshot, when it changed.
function renderSnapshot() {
  if (!app.board || !app.map) return;
  const key = JSON.stringify([app.board.incidents, app.board.suspended, app.board.shares, app.inbox, app.phones]);
  if (key === app.snapKey) return;
  app.snapKey = key;
  const sms = boardSms(app.board, app.inbox);
  renderKpis($("#kpis"), metrics(app.board, sms));
  drawMap();
  renderFeed($("#feed"), feedItems(app.board, sms, app.phones, app.map), app.feedSeen);
  renderQueueView();
  analytics.render(app.board, serverNow());
}

let ticks = 0;

function tick() {
  for (const el of document.querySelectorAll(".clock[data-t1], .clock-q[data-t1]")) {
    el.firstChild.textContent = mmss(serverNow() - Date.parse(el.dataset.t1));
  }
  renderTimelines(false);
  for (const kind of KINDS) widgets[kind].refresh();
  refreshTabs();
  if (++ticks % 5 === 0) analytics.render(app.board, serverNow()); // an open stage grows
}

// ---------- Scenarios ----------

let runCtl = null;

function renderRunStatus() {
  const s = app.scenario;
  const sc = SCENARIOS.find((x) => x.id === s.id);
  const out = $("#run-status");
  out.className = "run-status " + s.phase;
  const text = {
    idle: () => "Pick a scenario to watch it play on the phones.",
    running: () => `${sc.title}. Step ${s.step + 1} of ${s.total}: ${s.label}.`,
    done: () => `${sc.title}: done. ${s.result}`,
    failed: () => `${sc.title} stopped at step ${s.step + 1}: ${s.reason}`,
    cancelled: () => `${sc.title} cancelled at step ${s.step + 1}.`,
  }[s.phase]();
  if (out.textContent !== text) out.textContent = text; // a live region: announce changes only
  $("#run-cancel").hidden = s.phase !== "running";
  for (const b of document.querySelectorAll(".scenario")) {
    b.disabled = s.phase === "running" || !live();
    b.classList.toggle("running", s.phase === "running" && b.dataset.id === s.id);
  }
  $("#ap-paused").hidden = !(ap.enabled && s.phase === "running");
}

// resetAndWait resets the demo and resolves once the new run is live.
async function resetAndWait(signal) {
  const before = app.run;
  const { status, data } = await api("POST", "api/reset", {});
  if (status !== 202) throw new Error(data.error || "The demo could not be reset.");
  app.phase = "resetting";
  clearRun();
  showPhase();
  while (!(live() && app.run > before && app.phones.length)) await sleep(400, signal);
}

async function play(sc) {
  if (app.scenario.phase === "running" || !live()) return;
  const n = app.board ? app.board.incidents.length : 0;
  if (sc.needsReset && n && !window.confirm(`"${sc.title}" starts from a fresh demo. Erase the ${n} incident(s) in the queue and start?`)) return;
  runCtl = new AbortController();
  const signal = runCtl.signal;
  const unlock = lockPhones(app.locks, phonesOf(sc.steps), `A scenario is using this phone: "${sc.title}". Cancel it to use the phone.`);
  ap.since = Infinity; // the autopilot leaves a scenario's alerts to the scenario
  const deps = {
    codes: app.codes, signal, now: () => Date.now(),
    smart: (kind) => app.modes[kind] === "smart",
    progress: (body) => api("POST", "api/progress", body),
    ussd: (phone, body, keys) => drive(phone, body, keys, signal, true),
    board: async () => { const r = await api("GET", "api/board"); return r.status === 200 ? r.data : null; },
    sleep: (ms) => sleep(ms, signal),
    reset: () => resetAndWait(signal),
  };
  renderPhones();
  await runScenario(sc, deps, (s) => { app.scenario = s; renderRunStatus(); });
  runCtl.abort(); // stops a parallel branch still running after its sibling failed
  unlock();
  ap.since = serverNow();
  renderPhones();
}

function buildScenarios() {
  $("#scenarios").replaceChildren(...SCENARIOS.map((sc) => h("button", { type: "button", class: "scenario", "data-id": sc.id,
    "aria-describedby": "sc-" + sc.id, onclick: () => play(sc) },
  h("b", { text: sc.title }), h("span", { id: "sc-" + sc.id, text: sc.description }))));
  $("#run-cancel").addEventListener("click", () => runCtl?.abort());
  renderRunStatus();
}

// ---------- Responder autopilot ----------

const ap = { ...AUTOPILOT, since: Infinity, decided: new Set(), queue: [], busy: false };

function apLog(text) { $("#ap-log").textContent = text; }

// autopilotPlan takes the decisions for alerts it has not seen; called on
// every board poll, it plans each alert once (ap.decided).
function autopilotPlan() {
  if (!ap.enabled || !app.board || app.scenario.phase === "running") return;
  for (const d of plan(app.board, app.phones, ap, ap.decided, ap.since)) {
    ap.decided.add(d.key);
    if (d.action === "ignore") apLog(`${d.responder} leaves the alert for ${d.ref} unanswered.`);
    else ap.queue.push(d);
  }
  ap.queue.sort((a, b) => a.at - b.at);
  pumpAutopilot();
}

// pumpAutopilot answers the queue one alert at a time, each when its delay
// is up, so each responder widget shows one answer at a time.
async function pumpAutopilot() {
  if (ap.busy) return;
  ap.busy = true;
  try {
    while (ap.enabled && ap.queue.length) {
      const d = ap.queue[0];
      if (app.scenario.phase === "running" || d.at > serverNow()) { await sleep(500); continue; }
      ap.queue.shift();
      const inc = app.board && app.board.incidents.find((i) => i.reference === d.ref);
      if (!inc || !inc.alerts.some((a) => a.responder === d.responder && a.outcome === "pending")) {
        apLog(`${d.responder}'s alert for ${d.ref} ended before the autopilot answered.`);
        continue;
      }
      const unlock = lockPhones(app.locks, [d.phone], "The autopilot is answering an alert on this phone.");
      renderPhones();
      const gen = app.gen;
      try {
        const deps = { codes: app.codes, signal: new AbortController().signal, ussd: (phone, body, keys) => drive(phone, body, keys, undefined, false) };
        await answerAlert(deps, d.phone, d.ref, d.action);
        if (gen === app.gen) apLog(`${d.responder} ${d.action === "accept" ? "accepted" : "declined"} ${d.ref} on ${app.codes.responder}.`);
      } catch (err) {
        if (gen === app.gen) apLog(`${d.responder} did not answer ${d.ref}: ${err.message}`);
      } finally {
        unlock();
        renderPhones();
      }
    }
  } finally {
    ap.busy = false;
  }
}

function buildAutopilot() {
  const shares = ["accept", "decline", "ignore"];
  const showShares = () => {
    const total = shares.reduce((n, k) => n + ap[k], 0);
    for (const k of shares) $(`#ap-${k}-v`).textContent = total ? Math.round((100 * ap[k]) / total) + "%" : "0%";
  };
  for (const k of shares) {
    const input = $(`#ap-${k}`);
    input.value = String(ap[k]);
    input.addEventListener("input", () => { ap[k] = Number(input.value); showShares(); });
  }
  const delay = $("#ap-delay");
  delay.value = String(ap.delayMs);
  delay.addEventListener("change", () => { ap.delayMs = Number(delay.value); });
  $("#ap-on").addEventListener("change", (e) => {
    ap.enabled = e.target.checked;
    ap.since = app.scenario.phase === "running" ? Infinity : serverNow();
    ap.queue = [];
    apLog(ap.enabled ? "On. New alerts are answered on the responder phones after the delay." : "Off. Alerts wait for a person to answer.");
    renderRunStatus();
    autopilotPlan();
  });
  showShares();
}

// ---------- Audit and reset ----------

async function verify() {
  if (app.verifying) return; // one verification at a time
  const btn = $("#audit-btn");
  const out = $("#audit-result");
  app.verifying = true;
  btn.disabled = true;
  out.hidden = false;
  out.className = "audit";
  out.replaceChildren(h("p", { text: "Signing a checkpoint and verifying the ledger…" }));
  out.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "nearest" });
  const gen = app.gen;
  const { status, data: r } = await api("POST", "api/audit", {});
  app.verifying = false;
  btn.disabled = !live();
  if (gen !== app.gen) return; // the run was reset while the ledger was verified
  if (status !== 200) {
    out.className = "audit fail";
    out.replaceChildren(h("h3", { text: "Verification did not run" }), h("p", { text: r.error || "Try again." }));
    return;
  }
  out.className = "audit " + (r.ok ? "pass" : "fail");
  const lines = [
    r.entries + " ledger entries in an unbroken SHA-256 hash chain" + (r.firstBadSeq ? ", broken at entry " + r.firstBadSeq : ""),
    r.checkpointSeq >= 0 ? "Checkpoint at entry " + r.checkpointSeq + " signed with ML-DSA-65 and written to the witness" : "Nothing to sign yet: the ledger is empty",
    r.checkpointsOk + " signed checkpoint(s) and " + r.witnessesOk + " witness note(s) verified" + (r.checkpointsBad + r.witnessesBad ? ", " + (r.checkpointsBad + r.witnessesBad) + " failed" : ""),
    r.events + " incident events; " + r.pending + " waiting for the relay",
    r.projections + " incident projection(s) rebuilt from their events" + (r.drift ? ", " + r.drift + " differ" : ", all match"),
  ];
  if (r.problem) lines.push("Problem: " + r.problem);
  out.replaceChildren(h("h3", { text: r.ok ? "Audit ledger verified" : "Audit ledger verification FAILED" }),
    h("ul", {}, lines.map((l) => h("li", { text: l }))));
}

async function reset() {
  if (!window.confirm("Erase every incident and message and start the demo again?")) return;
  const { status, data } = await api("POST", "api/reset", {});
  if (status !== 202) {
    window.alert(data.error || "The demo could not be reset.");
    return;
  }
  app.phase = "resetting";
  clearRun();
  showPhase();
}

// clearRun forgets everything that belonged to the run that ended, whether
// this browser or another one reset it: sessions, inboxes, queue, audit,
// reports and the autopilot's queue.
function clearRun() {
  app.gen++;
  app.inbox = {};
  app.seen = {};
  app.cleared = {};
  app.shareTried = new Map();
  app.phones = [];
  app.board = null;
  app.snapKey = "";
  app.incident = "";
  app.hover = "";
  app.closing = null;
  app.reports = {};
  app.feedSeen = new Set();
  ap.queue = [];
  ap.decided = new Set();
  $("#audit-result").hidden = true;
  for (const id of ["#queue", "#queue-detail", "#queue-filters", "#kpis", "#map", "#feed"]) $(id).replaceChildren();
  analytics = new Analytics($("#analytics"));
  $("#map-summary").textContent = "";
  for (const kind of KINDS) widgets[kind].setPhone(null);
}

// ---------- Polling ----------

// live: the server last said ready, and is still answering.
function live() {
  return app.phase === "ready" && !app.lost;
}

function showPhase(down) {
  const s = $("#status");
  const ready = live();
  const text = app.lost ? "Connection lost; retrying…" : {
    starting: "Starting the system…", resetting: "Resetting: erasing and starting again…",
    failed: "The system did not start. Try Reset.", ready: "Live: intake-gw, dispatch-worker and outbox-relay running",
  }[app.phase] || "Connecting…";
  s.textContent = down && down.length ? "Stopped: " + down.join(", ") + ". Press Reset." : text;
  s.className = "status " + (ready && !(down && down.length) ? "ok" : app.lost || app.phase === "failed" || (down && down.length) ? "bad" : "");
  $("#audit-btn").disabled = !ready || app.verifying;
  $("#reset-btn").disabled = app.phase === "starting" || app.phase === "resetting" || app.scenario.phase === "running";
  for (const kind of KINDS) widgets[kind].render(true);
  renderRunStatus();
}

// The page's sessions, inboxes, queue and audit result belong to one ready
// run, named by the server's run counter. Any other state the server reports
// ends that run on the page: a reset here or in another browser, a start
// that failed, a run the page has not seen. A failed poll changes nothing,
// so the next successful one still sees whatever happened meanwhile.
async function pollState() {
  const { status, data } = await api("GET", "api/state");
  app.lost = status !== 200;
  if (app.lost) { showPhase(); return; }
  const was = app.phase;
  app.phase = data.phase;
  if (data.phase !== "ready" || data.run !== app.run) clearRun();
  app.run = data.run;
  if (data.codes) app.codes = data.codes;
  if (data.map) app.map = data.map;
  if (data.dispatch?.cascadeTimeoutMs) app.timeoutMs = data.dispatch.cascadeTimeoutMs;
  if (data.phase === "ready" && data.phones) {
    const changed = JSON.stringify(data.phones) !== JSON.stringify(app.phones) || was !== "ready";
    app.phones = data.phones;
    if (changed) {
      for (const kind of KINDS) {
        const mine = app.phones.filter((p) => kindOf(p) === kind);
        if (!mine.some((p) => p.id === app.selected[kind])) app.selected[kind] = mine.length ? mine[0].id : "";
        widgets[kind].setPhone(app.phones.find((p) => p.id === app.selected[kind]));
      }
      buildAllTabs();
      renderHints();
    }
  }
  showPhase(data.down);
}

async function pollData() {
  if (!live()) return;
  const gen = app.gen;
  const [sms, board] = await Promise.all([api("GET", "api/sms"), api("GET", "api/board")]);
  if (gen !== app.gen) return; // started before a reset: the data belongs to the old run
  if (board.status === 200) {
    app.skew = new Date(board.data.now) - Date.now();
    app.board = board.data;
    autopilotPlan();
    autoShare();
  }
  if (sms.status === 200) {
    app.inbox = sms.data;
    for (const kind of KINDS) widgets[kind].renderInbox();
  }
  for (const kind of KINDS) widgets[kind].refresh();
  refreshTabs();
  renderSnapshot();
  $("#board-stale").hidden = board.status === 200; // a failed refresh is shown, not hidden behind the last copy
}

function loop(fn, ms) {
  const run = async () => { await fn(); setTimeout(run, ms); };
  run();
}

let analytics = new Analytics($("#analytics"));
for (const kind of KINDS) makeWidget(kind);
buildModes();
renderHints();
$("#audit-btn").addEventListener("click", verify);
$("#reset-btn").addEventListener("click", reset);
buildScenarios();
buildAutopilot();
new ResizeObserver(() => { if (app.board && app.map) drawMap(); }).observe($("#map"));
new ResizeObserver(() => analytics.render(app.board, serverNow())).observe($("#analytics"));
loop(pollState, 2000);
loop(pollData, 1500);
setInterval(tick, 1000);
