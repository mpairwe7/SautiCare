// Sauti Dispatch demo UI: the composition root. It polls one snapshot
// ({state, board, sms}) and renders every view from it; every simulated
// action goes through the same USSD API as the on-screen phones. Every
// value from the server is set as text, never as HTML. The server only ever
// sends masked phone numbers.
import { h, clock } from "./dom.js";
import { mmss } from "./format.js";
import { metrics } from "./metrics.js";
import { feedItems } from "./feed.js";
import { layout } from "./geo.js";
import { renderKpis, renderMap, renderFeed, highlight } from "./views.js";
import { renderIncident, timelineChart } from "./cards.js";
import { SCENARIOS, runScenario, phonesOf, answerAlert, IDLE } from "./scenarios.js";
import { AUTOPILOT, plan } from "./autopilot.js";

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

// sleep resolves after ms, or rejects once signal is aborted.
function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason); return; }
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => { clearTimeout(t); reject(signal.reason); }, { once: true });
  });
}

const app = {
  phase: "", // the last phase the server reported; a failed poll leaves it as it was
  run: 0,
  lost: false, // the last state poll failed
  verifying: false, // an audit verification is in flight
  phones: [],
  codes: { caller: "*384*1#", responder: "*384*2#" },
  map: null, // the gazetteer, from /api/state
  board: null,
  inbox: {},
  seen: {},
  selected: { caller: "caller-a", responder: "" },
  open: new Set(),
  skew: 0, // server clock minus browser clock, ms
  snapKey: "",
  gen: 0, // bumped by clearRun: a response to a request from an earlier run is dropped
  incident: "", // the incident picked on the map
  feedSeen: new Set(),
  driven: new Map(), // phone → why its keys are locked: a scenario or the autopilot is dialling it
  scenario: IDLE,
};

const serverNow = () => Date.now() + app.skew;

// ---------- Phones ----------

const READ_MS = 650; // the audience reads a screen before the next key
const KEY_MS = 110;

class Phone {
  constructor(mount, kind) {
    this.kind = kind;
    this.p = null;
    this.mode = "idle"; // idle | con | end
    this.session = "";
    this.busy = false;
    this.gen = 0; // bumped when the phone changes or hangs up: a late reply is then dropped
    const id = "in-" + kind;
    this.text = h("p", { class: "screen-text", "aria-live": "polite", "aria-atomic": "true" });
    this.meta = h("span", { class: "screen-meta" });
    this.label = h("label", { class: "screen-meta", for: id, text: "Number" });
    this.input = h("input", { id, class: "screen-input", autocomplete: "off", spellcheck: "false", maxlength: "64", inputmode: "tel",
      onkeydown: (e) => {
        if (e.key === "Enter") { e.preventDefault(); this.primary(); }
        if (e.key === "Escape") { e.preventDefault(); this.end(); }
      } });
    this.note = h("p", { class: "screen-note", role: "alert", hidden: true });
    this.lock = h("p", { class: "phone-lock", hidden: true });
    this.callBtn = h("button", { type: "button", class: "key-call", onclick: () => this.primary() });
    this.endBtn = h("button", { type: "button", class: "key-end", text: "End", onclick: () => this.end() });
    const keys = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];
    const names = { "*": "Star", "#": "Hash" };
    this.keypad = h("div", { class: "keypad", role: "group", "aria-label": "Keypad" },
      keys.map((k) => h("button", { type: "button", text: k, "aria-label": names[k] || k, onclick: () => this.press(k) })));
    this.quick = h("button", { type: "button", class: "quick", onclick: () => this.dial(this.code()) });
    this.name = h("span", { class: "phone-name" });
    this.number = h("span", { class: "phone-num" });
    this.root = h("article", { class: "phone" },
      h("div", { class: "phone-head" }, this.name, this.number),
      h("div", { class: "screen" }, this.meta, this.text, this.note, this.label, this.input),
      h("div", { class: "soft" }, this.callBtn, this.endBtn),
      this.keypad, this.quick);
    this.msgs = h("ol", { class: "msgs" });
    this.empty = h("p", { class: "empty", text: "No messages yet." });
    this.count = h("span", { class: "badge", hidden: true });
    this.inbox = h("section", { class: "inbox", "aria-label": "SMS inbox" },
      h("h3", {}, "SMS inbox", this.count), this.empty, this.msgs);
    mount.replaceChildren(this.lock, this.root, this.inbox);
    this.render();
  }

  code() { return this.kind === "caller" ? app.codes.caller : app.codes.responder; }

  setPhone(p) {
    if (this.p && p && this.p.id === p.id) return;
    this.p = p;
    this.gen++;
    this.mode = "idle";
    this.session = "";
    this.lastCount = undefined;
    this.render();
    this.renderInbox();
  }

  locked() { return this.p ? app.driven.get(this.p.id) : undefined; }

  press(k) {
    if (this.mode === "end" || this.locked()) return;
    this.input.value += k;
    this.input.focus();
  }

  primary() {
    if (this.busy || !this.p || !live() || this.locked()) return; // Enter must not act while the page is not live
    if (this.mode === "idle") this.dial(this.input.value.trim());
    else if (this.mode === "con") this.reply(this.input.value);
    else this.end();
  }

  end() {
    // Hanging up a USSD session simply abandons it, as on a handset.
    if (this.locked()) return;
    this.gen++;
    this.mode = "idle";
    this.session = "";
    this.note.hidden = true;
    this.render();
  }

  async dial(code) {
    if (!code || !this.p || this.busy) return;
    await this.send({ phone: this.p.id, code });
  }

  async reply(input) {
    await this.send({ session: this.session, input });
  }

  async send(body) {
    const gen = this.gen;
    this.busy = true;
    this.note.hidden = true;
    this.render();
    const { status, data } = await api("POST", "api/ussd", body);
    this.busy = false;
    if (gen !== this.gen) {
      // Another phone was selected, or this one hung up, while the reply was on its way.
      this.render();
      return;
    }
    this.show(status, data);
    if (this.mode === "con") this.input.focus();
    else this.callBtn.focus();
  }

  // show puts a USSD answer on the screen, from a key typed here or one a
  // scenario or the autopilot sent.
  show(status, data) {
    if (status === 200 || status === 410) {
      this.mode = data.end ? "end" : "con";
      this.session = data.session || "";
      this.screen = data.text || "";
      this.input.value = "";
    } else {
      this.note.textContent = data.error || "Something went wrong.";
      this.note.hidden = false;
    }
    this.render();
  }

  // type shows keys going into the reply box, as a person would press them.
  async type(keys, signal) {
    this.input.value = "";
    await sleep(READ_MS, signal);
    for (const k of keys) {
      this.input.value += k;
      await sleep(KEY_MS, signal);
    }
  }

  render() {
    const p = this.p;
    this.name.textContent = p ? p.label : "…";
    this.number.textContent = p ? [p.ward, p.number].filter(Boolean).join(" · ") : "";
    this.root.setAttribute("aria-label", p ? p.label + "'s phone" : "Phone");
    this.quick.textContent = "Dial " + this.code();
    const idle = this.mode === "idle";
    if (idle) {
      this.meta.textContent = "Ready";
      this.text.textContent = this.kind === "caller"
        ? "Dial " + app.codes.caller + "\nto report an emergency."
        : "Dial " + app.codes.responder + "\nfor the responder menu.";
      this.label.textContent = "Number to dial";
    } else {
      this.meta.textContent = this.mode === "con" ? "USSD" : "USSD — session ended";
      this.text.textContent = this.screen;
      this.label.textContent = "Reply";
    }
    const lock = this.locked();
    this.lock.textContent = lock || "";
    this.lock.hidden = !lock;
    this.root.classList.toggle("driven", Boolean(lock));
    this.label.hidden = this.input.hidden = this.mode === "end";
    this.input.setAttribute("inputmode", idle ? "tel" : "text");
    this.callBtn.textContent = idle ? "Call" : this.mode === "con" ? "Send" : "OK";
    const off = this.busy || !p || !live() || Boolean(lock);
    for (const b of [this.callBtn, this.endBtn, this.quick, ...this.keypad.children]) b.disabled = off;
    this.input.disabled = off;
    this.quick.hidden = !idle;
  }

  renderInbox() {
    const list = (this.p && app.inbox[this.p.id]) || [];
    const fresh = this.lastCount !== undefined && list.length > this.lastCount;
    this.lastCount = list.length;
    app.seen[this.p ? this.p.id : ""] = list.length;
    this.empty.hidden = list.length > 0;
    this.count.hidden = list.length === 0;
    this.count.textContent = String(list.length);
    const items = list.slice().reverse().map((m, i) =>
      h("li", { class: fresh && i === 0 ? "new" : "" },
        h("time", { datetime: m.at, text: clock(new Date(m.at)) }), h("p", { text: m.text })));
    this.msgs.replaceChildren(...items);
  }
}

// ---------- Tabs ----------

let callerPhone, respPhone;

// buildTabs builds a phone selector once per phone list; refreshTabs keeps
// its selection and unread badges current without replacing the buttons, so
// keyboard focus stays put.
function buildTabs(mount, phones, kind) {
  mount.replaceChildren(...phones.map((p) => h("button", { type: "button", role: "tab", class: "tab", id: "tab-" + p.id,
    "data-id": p.id, onclick: () => select(kind, p.id) }, p.label, h("span", { class: "badge", hidden: true }))));
  mount.onkeydown = (e) => {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
    const i = phones.findIndex((p) => p.id === app.selected[kind]);
    const next = phones[(i + (e.key === "ArrowRight" ? 1 : phones.length - 1)) % phones.length];
    select(kind, next.id);
    document.getElementById("tab-" + next.id).focus();
  };
}

function refreshTabs() {
  for (const tab of document.querySelectorAll(".tab")) {
    const id = tab.dataset.id;
    const sel = id === app.selected.caller || id === app.selected.responder;
    const unread = sel ? 0 : Math.max(0, (app.inbox[id] || []).length - (app.seen[id] || 0));
    tab.setAttribute("aria-selected", String(sel));
    tab.tabIndex = sel ? 0 : -1;
    tab.classList.toggle("driven", app.driven.has(id));
    const badge = tab.querySelector(".badge");
    badge.hidden = unread === 0;
    badge.textContent = String(unread);
    badge.setAttribute("aria-label", unread + " new");
  }
}

const widget = (kind) => (kind === "caller" ? callerPhone : respPhone);

function select(kind, id) {
  app.selected[kind] = id;
  widget(kind).setPhone(app.phones.find((p) => p.id === id));
  refreshTabs();
}

function buildAllTabs() {
  buildTabs($("#caller-tabs"), app.phones.filter((p) => p.kind === "caller"), "caller");
  buildTabs($("#resp-tabs"), app.phones.filter((p) => p.kind === "responder"), "responder");
  refreshTabs();
}

function renderPhones() {
  callerPhone.render();
  respPhone.render();
  refreshTabs();
}

// drive sends one USSD request for a scenario or the autopilot, showing it
// on that phone's widget: the widget switches to the phone, the keys appear
// one by one, then the answer. The autopilot leaves a widget alone while a
// person is part way through a session on another phone; it then sends the
// request without showing it.
async function drive(phoneId, body, keys, signal, takeOver) {
  const p = app.phones.find((x) => x.id === phoneId);
  if (!p) return { status: 0, data: { error: `${phoneId} is not on screen.` } };
  const w = widget(p.kind);
  const person = w.p && w.p.id !== phoneId && w.mode === "con" && !app.driven.has(w.p.id);
  const shown = takeOver || !person;
  if (shown) {
    if (!w.p || w.p.id !== phoneId) select(p.kind, phoneId);
    await w.type(keys, signal);
  }
  const res = await api("POST", "api/ussd", body);
  if (shown && w.p && w.p.id === phoneId) w.show(res.status, res.data);
  return res;
}

// ---------- Snapshot views ----------

function onSelectIncident(id) {
  app.incident = id;
  highlight($("#map"), id);
  for (const card of document.querySelectorAll(".incident")) card.classList.toggle("selected", card.dataset.inc === id);
  const card = document.getElementById("inc-" + id);
  if (!card) return;
  card.scrollIntoView({ behavior: reducedMotion() ? "auto" : "smooth", block: "center" });
  card.focus({ preventScroll: true });
}

const onHoverIncident = (id) => highlight($("#map"), id || app.incident);

function renderBoard(b) {
  $("#suspended").hidden = !b.suspended;
  const board = $("#board");
  if (!b.incidents.length) {
    board.replaceChildren(h("p", { class: "placeholder", text: "No incidents yet. Dial " + app.codes.caller + " on the caller phone, or play a scenario." }));
    return;
  }
  const ctx = { open: app.open, now: serverNow(), selected: app.incident, onHover: onHoverIncident };
  board.replaceChildren(...b.incidents.map((inc) => renderIncident(inc, ctx)));
  renderTimelines(true);
}

// renderTimelines redraws the timeline charts at their mounts' width: every
// one after a board change, then each second the open ones, whose current
// interval runs to now.
function renderTimelines(all) {
  if (!app.board) return;
  const byId = Object.fromEntries(app.board.incidents.map((i) => [i.id, i]));
  for (const mount of document.querySelectorAll(".tl-mount")) {
    const inc = byId[mount.dataset.tl];
    if (!inc || (!all && (inc.state === "closed" || inc.state === "merged"))) continue;
    mount.replaceChildren(timelineChart(inc, serverNow(), mount.clientWidth) || "");
  }
}

// renderSnapshot renders every view of the snapshot, when it changed.
function renderSnapshot() {
  if (!app.board || !app.map) return;
  const key = JSON.stringify([app.board.incidents, app.board.suspended, app.inbox, app.phones]);
  if (key === app.snapKey) return;
  app.snapKey = key;
  renderKpis($("#kpis"), metrics(app.board, app.inbox));
  renderMap($("#map"), layout(app.map, app.phones, app.board), app.incident, onSelectIncident);
  renderFeed($("#feed"), feedItems(app.board, app.inbox, app.phones, app.map), app.feedSeen);
  renderBoard(app.board);
}

function tick() {
  for (const el of document.querySelectorAll(".clock[data-t1]")) {
    el.firstChild.textContent = mmss(serverNow() - Date.parse(el.dataset.t1));
  }
  renderTimelines(false);
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
  if (sc.needsReset && n && !window.confirm(`"${sc.title}" starts from a fresh demo. Erase the ${n} incident(s) on the board and start?`)) return;
  runCtl = new AbortController();
  const signal = runCtl.signal;
  const lock = `A scenario is dialling this phone: "${sc.title}". Cancel it to use the keys.`;
  for (const id of phonesOf(sc.steps)) app.driven.set(id, lock);
  ap.since = Infinity; // the autopilot leaves a scenario's alerts to the scenario
  const deps = {
    codes: app.codes, signal, now: () => Date.now(),
    ussd: (phone, body, keys) => drive(phone, body, keys, signal, true),
    board: async () => { const r = await api("GET", "api/board"); return r.status === 200 ? r.data : null; },
    sleep: (ms) => sleep(ms, signal),
    reset: () => resetAndWait(signal),
  };
  renderPhones();
  await runScenario(sc, deps, (s) => { app.scenario = s; renderRunStatus(); });
  runCtl.abort(); // stops a parallel branch still running after its sibling failed
  for (const id of phonesOf(sc.steps)) app.driven.delete(id);
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
// is up, so the responder widget shows one answer at a time.
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
      app.driven.set(d.phone, "The autopilot is answering an alert on this phone.");
      renderPhones();
      const gen = app.gen;
      try {
        const deps = { codes: app.codes, signal: new AbortController().signal, ussd: (phone, body, keys) => drive(phone, body, keys, undefined, false) };
        await answerAlert(deps, d.phone, d.ref, d.action);
        if (gen === app.gen) apLog(`${d.responder} ${d.action === "accept" ? "accepted" : "declined"} ${d.ref} on ${app.codes.responder}.`);
      } catch (err) {
        if (gen === app.gen) apLog(`${d.responder} did not answer ${d.ref}: ${err.message}`);
      } finally {
        app.driven.delete(d.phone);
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
    r.entries + " ledger entries in an unbroken SHA-256 hash chain" + (r.firstBadSeq ? " — broken at entry " + r.firstBadSeq : ""),
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
// this browser or another one reset it: sessions, inboxes, board, audit,
// and the autopilot's queue.
function clearRun() {
  app.gen++;
  app.inbox = {};
  app.seen = {};
  app.phones = [];
  app.board = null;
  app.snapKey = "";
  app.incident = "";
  app.feedSeen = new Set();
  ap.queue = [];
  ap.decided = new Set();
  $("#audit-result").hidden = true;
  for (const id of ["#board", "#kpis", "#map", "#feed"]) $(id).replaceChildren();
  $("#map-summary").textContent = "";
  callerPhone.setPhone(null);
  respPhone.setPhone(null);
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
  callerPhone.render();
  respPhone.render();
  renderRunStatus();
}

// The page's sessions, inboxes, board and audit result belong to one ready
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
  if (data.phase === "ready" && data.phones) {
    const changed = JSON.stringify(data.phones) !== JSON.stringify(app.phones) || was !== "ready";
    app.phones = data.phones;
    if (changed) {
      const resps = app.phones.filter((p) => p.kind === "responder");
      if (!resps.some((p) => p.id === app.selected.responder)) app.selected.responder = resps.length ? resps[0].id : "";
      callerPhone.setPhone(app.phones.find((p) => p.id === app.selected.caller));
      respPhone.setPhone(app.phones.find((p) => p.id === app.selected.responder));
      buildAllTabs();
    }
  }
  showPhase(data.down);
}

async function pollData() {
  if (!live()) return;
  const gen = app.gen;
  const [sms, board] = await Promise.all([api("GET", "api/sms"), api("GET", "api/board")]);
  if (gen !== app.gen) return; // started before a reset: the data belongs to the old run
  if (sms.status === 200) {
    app.inbox = sms.data;
    callerPhone.renderInbox();
    respPhone.renderInbox();
    refreshTabs();
  }
  if (board.status === 200) {
    app.skew = new Date(board.data.now) - Date.now();
    app.board = board.data;
    autopilotPlan();
  }
  renderSnapshot();
  $("#board-stale").hidden = board.status === 200; // a failed refresh is shown, not hidden behind the last copy
}

function loop(fn, ms) {
  const run = async () => { await fn(); setTimeout(run, ms); };
  run();
}

callerPhone = new Phone($("#caller-phone"), "caller");
respPhone = new Phone($("#resp-phone"), "responder");
$("#audit-btn").addEventListener("click", verify);
$("#reset-btn").addEventListener("click", reset);
buildScenarios();
buildAutopilot();
loop(pollState, 2000);
loop(pollData, 1500);
setInterval(tick, 1000);
