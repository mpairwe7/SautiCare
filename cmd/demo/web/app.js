"use strict";
// Sauti Dispatch demo UI. Every value from the server is set as text, never
// as HTML. The server only ever sends masked phone numbers.

const $ = (sel) => document.querySelector(sel);

// h builds an element: h("p", {class: "x", text: "y"}, child, ...).
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "text") el.textContent = v;
    else if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) if (kid !== null && kid !== undefined && kid !== false) el.append(kid);
  return el;
}

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

const app = {
  phase: "",
  phones: [],
  codes: { caller: "*384*1#", responder: "*384*2#" },
  inbox: {},
  seen: {},
  selected: { caller: "caller-a", responder: "" },
  open: new Set(),
  skew: 0, // server clock minus browser clock, ms
  boardKey: "",
};

// ---------- Phones ----------

class Phone {
  constructor(mount, kind) {
    this.kind = kind;
    this.p = null;
    this.mode = "idle"; // idle | con | end
    this.session = "";
    this.busy = false;
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
    mount.replaceChildren(this.root, this.inbox);
    this.render();
  }

  code() { return this.kind === "caller" ? app.codes.caller : app.codes.responder; }

  setPhone(p) {
    if (this.p && p && this.p.id === p.id) return;
    this.p = p;
    this.mode = "idle";
    this.session = "";
    this.lastCount = undefined;
    this.render();
    this.renderInbox();
  }

  press(k) {
    if (this.mode === "end") return;
    this.input.value += k;
    this.input.focus();
  }

  primary() {
    if (this.busy || !this.p) return;
    if (this.mode === "idle") this.dial(this.input.value.trim());
    else if (this.mode === "con") this.reply(this.input.value);
    else this.end();
  }

  end() {
    // Hanging up a USSD session simply abandons it, as on a handset.
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
    this.busy = true;
    this.note.hidden = true;
    this.render();
    const { status, data } = await api("POST", "api/ussd", body);
    this.busy = false;
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
    if (this.mode === "con") this.input.focus();
    else this.callBtn.focus();
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
    this.label.hidden = this.input.hidden = this.mode === "end";
    this.input.setAttribute("inputmode", idle ? "tel" : "text");
    this.callBtn.textContent = idle ? "Call" : this.mode === "con" ? "Send" : "OK";
    for (const b of [this.callBtn, this.endBtn, this.quick, ...this.keypad.children]) b.disabled = this.busy || !p || app.phase !== "ready";
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

function clock(d) {
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
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
    const badge = tab.querySelector(".badge");
    badge.hidden = unread === 0;
    badge.textContent = String(unread);
    badge.setAttribute("aria-label", unread + " new");
  }
}

function select(kind, id) {
  app.selected[kind] = id;
  (kind === "caller" ? callerPhone : respPhone).setPhone(app.phones.find((p) => p.id === id));
  refreshTabs();
}

function buildAllTabs() {
  buildTabs($("#caller-tabs"), app.phones.filter((p) => p.kind === "caller"), "caller");
  buildTabs($("#resp-tabs"), app.phones.filter((p) => p.kind === "responder"), "responder");
  refreshTabs();
}

// ---------- Board ----------

const tNames = { T1: "call", T2: "ready to dispatch", T3: "first alert", T4: "accepted", T5: "arrived", T6: "departed", T7: "at facility" };
const stateNames = { intake: "Taking the call", triaged: "Triaged", dispatching: "Finding help", assigned: "Help assigned",
  on_scene: "On scene", transporting: "Transporting", at_facility: "At facility", closed: "Closed", merged: "Merged" };
const outcomeNames = { pending: "waiting", accepted: "accepted", declined: "declined", no_response: "no response", stood_down: "stood down" };

function since(from, to) {
  const s = Math.max(0, Math.round((to - from) / 1000));
  const m = Math.floor(s / 60);
  return m + ":" + String(s % 60).padStart(2, "0");
}

function details(key, summary, body) {
  const d = h("details", { open: app.open.has(key), ontoggle: () => (d.open ? app.open.add(key) : app.open.delete(key)) },
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

function renderIncident(inc) {
  const t1 = inc.t.T1 ? new Date(inc.t.T1) : null;
  const sev = inc.severity || "none";
  const open = inc.state !== "closed" && inc.state !== "merged";
  const clockEl = h("span", { class: "clock", "data-t1": t1 && open ? inc.t.T1 : null },
    t1 ? since(t1, open ? Date.now() + app.skew : Math.max(...Object.values(inc.t).map((v) => new Date(v)))) : "—",
    h("small", { text: open ? "since T1" : "T1 to last" }));
  const facts = [inc.category, inc.ward, inc.landmark].filter(Boolean).join(" · ") || "Answers not given yet";
  const tags = [];
  if (inc.escalated) tags.push(h("li", { class: "esc", text: "Escalated to a coordinator" }));
  for (const f of inc.flags || []) tags.push(h("li", { text: f.replaceAll("_", " ") }));
  if (inc.location) tags.push(h("li", { text: "located by " + inc.location }));
  if (inc.wake && open) tags.push(h("li", { text: "next dispatch step " + clock(new Date(inc.wake)) }));
  const ttimes = h("ol", { class: "ttimes", "aria-label": "Protocol timestamps" },
    ["T1", "T2", "T3", "T4", "T5", "T6", "T7"].map((k) => {
      const v = inc.t[k];
      return h("li", { class: v ? "done" : "" }, h("b", { text: k }),
        h("span", { class: "v", text: v && t1 ? "+" + since(t1, new Date(v)) : "—" }), " " + tNames[k]);
    }));
  const alerts = details(inc.id + ":alerts", "Alerts (" + inc.alerts.length + ")",
    inc.alerts.length ? table(["Responder", "Ring", "Sent", "Outcome", "Delivery"], inc.alerts.map((a) => [
      a.responder + " (" + a.role + ")", String(a.ring), clock(new Date(a.sentAt)),
      outcomeNames[a.outcome] + (a.reason ? " — " + a.reason : "") + (a.respondedAt ? " at " + clock(new Date(a.respondedAt)) : ""),
      attempts(a.attempts)])) : h("p", { class: "empty", text: "No responder alerted yet." }));
  const kids = [alerts];
  if (inc.escalations.length) {
    kids.push(details(inc.id + ":esc", "Escalations (" + inc.escalations.length + ")",
      table(["Time", "Reason", "Role"], inc.escalations.map((e) => [clock(new Date(e.at)), e.reason.replaceAll("_", " "), e.role || "—"]))));
  }
  if (inc.notices.length) {
    kids.push(details(inc.id + ":notices", "Caller notices (" + inc.notices.length + ")",
      table(["Notice", "Channel", "Status"], inc.notices.map((n) => [n.kind.replaceAll("_", " "), n.channel, n.status + (n.delivery ? " (" + n.delivery + ")" : "")]))));
  }
  kids.push(details(inc.id + ":events", "Event timeline (" + inc.events.length + ")",
    table(["#", "Time", "Event", "Actor", "Coded payload"], inc.events.map((e) => [
      String(e.seq), clock(new Date(e.at)), e.type, e.actor, h("span", { class: "payload", text: e.payload })]))));
  return h("article", { class: "incident " + sev, "aria-labelledby": "ref-" + inc.id },
    h("div", { class: "inc-head" },
      h("span", { class: "chip " + sev, text: sev === "none" ? "UNCLASSIFIED" : sev.toUpperCase() }),
      h("h3", { id: "ref-" + inc.id, "aria-label": "Incident " + (inc.reference || ""), text: inc.reference || "—" }),
      h("span", { class: "state", text: stateNames[inc.state] || inc.state }),
      clockEl),
    h("p", { class: "facts", text: facts }),
    tags.length ? h("ul", { class: "tags" }, tags) : null,
    ttimes, kids);
}

function renderBoard(b) {
  app.skew = new Date(b.now) - Date.now();
  $("#suspended").hidden = !b.suspended;
  const key = JSON.stringify(b.incidents);
  if (key === app.boardKey) return;
  app.boardKey = key;
  const board = $("#board");
  if (!b.incidents.length) {
    board.replaceChildren(h("p", { class: "placeholder", text: "No incidents yet. Dial " + app.codes.caller + " on the caller phone to report one." }));
    return;
  }
  board.replaceChildren(...b.incidents.map(renderIncident));
}

function tick() {
  for (const el of document.querySelectorAll(".clock[data-t1]")) {
    el.firstChild.textContent = since(new Date(el.dataset.t1), Date.now() + app.skew);
  }
}

// ---------- Audit and reset ----------

async function verify() {
  const btn = $("#audit-btn");
  const out = $("#audit-result");
  btn.disabled = true;
  out.hidden = false;
  out.className = "audit";
  out.replaceChildren(h("p", { text: "Signing a checkpoint and verifying the ledger…" }));
  const { status, data: r } = await api("POST", "api/audit", {});
  btn.disabled = app.phase !== "ready";
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
  app.inbox = {};
  app.seen = {};
  app.boardKey = "";
  $("#audit-result").hidden = true;
  $("#board").replaceChildren();
  callerPhone.setPhone(null);
  respPhone.setPhone(null);
  showPhase();
}

// ---------- Polling ----------

function showPhase(down) {
  const s = $("#status");
  const ready = app.phase === "ready";
  const text = {
    starting: "Starting the system…", resetting: "Resetting: erasing and starting again…",
    failed: "The system did not start. Try Reset.", ready: "Live: intake-gw, dispatch-worker and outbox-relay running",
  }[app.phase] || "Connecting…";
  s.textContent = down && down.length ? "Stopped: " + down.join(", ") + ". Press Reset." : text;
  s.className = "status " + (ready && !(down && down.length) ? "ok" : app.phase === "failed" || (down && down.length) ? "bad" : "");
  $("#audit-btn").disabled = !ready;
  $("#reset-btn").disabled = app.phase === "starting" || app.phase === "resetting";
  callerPhone.render();
  respPhone.render();
}

async function pollState() {
  const { status, data } = await api("GET", "api/state");
  if (status !== 200) { app.phase = ""; showPhase(); return; }
  const was = app.phase;
  app.phase = data.phase;
  if (data.codes) app.codes = data.codes;
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
  if (app.phase !== "ready") return;
  const [sms, board] = await Promise.all([api("GET", "api/sms"), api("GET", "api/board")]);
  if (sms.status === 200) {
    app.inbox = sms.data;
    callerPhone.renderInbox();
    respPhone.renderInbox();
    refreshTabs();
  }
  if (board.status === 200) renderBoard(board.data);
}

function loop(fn, ms) {
  const run = async () => { await fn(); setTimeout(run, ms); };
  run();
}

document.addEventListener("DOMContentLoaded", () => {
  callerPhone = new Phone($("#caller-phone"), "caller");
  respPhone = new Phone($("#resp-phone"), "responder");
  $("#audit-btn").addEventListener("click", verify);
  $("#reset-btn").addEventListener("click", reset);
  loop(pollState, 2000);
  loop(pollData, 1500);
  setInterval(tick, 1000);
});
