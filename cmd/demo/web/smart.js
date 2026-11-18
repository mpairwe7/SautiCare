// A smartphone: the same Sauti service as the basic phone, shown as an app.
// Every choice is a key sent to the same USSD service (ussd.js turns each
// screen into buttons), so the system under the app is unchanged. What only
// a smartphone adds is shown for what it is:
//   - location: simulated GPS. The caller's picks the ward and nearest
//     place; a responder's starts by their registered stage, and a VHT's is
//     sent to the transporter with their facility and number (app.js);
//   - push notifications: drawn from the alert and the SMS the system sent;
//   - the steps after accepting (arrived, picked up, at facility): a demo
//     preview of M3, recorded through /api/progress.
import { h, svg, clock } from "./dom.js";
import { mmss, REF, payload } from "./format.js";
import { Inbox, sleep, READ_MS } from "./phone.js";
import { parseScreen, placeKind, progressOf } from "./ussd.js";
import { projector, cells, nearest, distanceM, trip, homeFix } from "./geo.js";
import { responderStatus, nextStep, partner, callerProgress, arrivedRoles } from "./responders.js";

const SEV = { red: "Red", yellow: "Yellow", green: "Green", none: "Unclassified" };
const OUTCOME = { pending: "waiting", accepted: "accepted", declined: "declined", no_response: "no response", stood_down: "stood down" };
const OUTCOME_TONE = { pending: "pending", accepted: "accepted", declined: "declined", no_response: "declined", stood_down: "none" };
const STEP_LABEL = { arrived: "I've arrived", departed: "Patient picked up, leaving now", at_facility: "Arrived at the facility" };
const STEP_DONE = { arrived: "Arrival recorded.", departed: "On the way to the facility.", at_facility: "Handed over. Job complete, thank you." };

// Where each caller SIM starts, as its simulated GPS fix: near a landmark of
// the gazetteer, a few metres off it.
const GPS_HOME = { "caller-a": "w01_clinic", "caller-b": "w02_market" };

// ---------- The mini map ----------

// miniMap draws the wards and a route: me (a blue dot), dest (a red pin), fac
// (a green square), vht (a purple dot, with a dashed line from me), at
// width × height. With onPick, every landmark is a button that calls
// onPick(landmark).
function miniMap(map, { me, dest, fac, vht, route, pick, onPick }, width = 264, height = 150) {
  if (!map) return null;
  const P = projector([...map.wards, ...map.landmarks], width, height, 16);
  const at = (p) => [P.x(p.lon), P.y(p.lat)];
  const centres = map.wards.map((w) => { const [x, y] = at(w); return { x, y }; });
  const polys = cells(centres, [0, 0, width, height]);
  const kids = [svg("g", {}, polys.map((poly, i) => svg("polygon", { class: `ward t${i % 4}`, points: poly.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(" ") })))];
  kids.push(svg("g", {}, map.landmarks.map((l) => {
    const [x, y] = at(l);
    if (!onPick) return svg("circle", { class: "lm" + (pick === l.id ? " pick" : ""), cx: x.toFixed(1), cy: y.toFixed(1), r: "2.5" });
    return svg("g", { class: "lm-btn", tabindex: "0", role: "button", "aria-label": `Set my location near ${l.name}`, onclick: () => onPick(l),
      onkeydown: (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onPick(l); } } },
    svg("circle", { cx: x.toFixed(1), cy: y.toFixed(1), r: "9", fill: "transparent" }),
    svg("circle", { class: "lm" + (pick === l.id ? " pick" : ""), cx: x.toFixed(1), cy: y.toFixed(1), r: "3.5" }));
  })));
  const pts = [route?.[0], route?.[1]].filter(Boolean).map(at);
  if (pts.length === 2) kids.push(svg("line", { class: "route", x1: pts[0][0].toFixed(1), y1: pts[0][1].toFixed(1), x2: pts[1][0].toFixed(1), y2: pts[1][1].toFixed(1) }));
  if (vht && me) {
    const [a, b] = [at(me), at(vht)];
    kids.push(svg("line", { class: "via", x1: a[0].toFixed(1), y1: a[1].toFixed(1), x2: b[0].toFixed(1), y2: b[1].toFixed(1) }));
  }
  if (vht) { const [x, y] = at(vht); kids.push(svg("circle", { class: "vhtpt", cx: x.toFixed(1), cy: y.toFixed(1), r: "6" })); }
  if (fac) { const [x, y] = at(fac); kids.push(svg("rect", { class: "fac", x: (x - 6).toFixed(1), y: (y - 6).toFixed(1), width: "12", height: "12" })); }
  if (dest) { const [x, y] = at(dest); kids.push(svg("circle", { class: "dest", cx: x.toFixed(1), cy: y.toFixed(1), r: "7" })); }
  if (me) { const [x, y] = at(me); kids.push(svg("circle", { class: "me-halo", cx: x.toFixed(1), cy: y.toFixed(1), r: "14" }), svg("circle", { class: "me", cx: x.toFixed(1), cy: y.toFixed(1), r: "6" })); }
  for (const [p, text] of [[dest, dest?.label], [fac, fac?.label], [vht, vht?.label]]) {
    if (!p || !text) continue;
    const [x, y] = at(p);
    kids.push(svg("text", { class: "lbl", x: (x + 10).toFixed(1), y: (y + 4).toFixed(1), "text-anchor": x > width - 90 ? "end" : "start",
      dx: x > width - 90 ? "-20" : "0", text }));
  }
  const name = [me && "your location", dest && `destination ${dest.label || ""}`, fac && `facility ${fac.label || ""}`, vht && `${vht.label || "the VHT"}`].filter(Boolean).join(", ");
  return h("div", { class: "sp-map" + (onPick ? " pickable" : "") },
    svg("svg", { viewBox: `0 0 ${width} ${height}`, role: onPick ? "group" : "img", "aria-label": onPick ? "Places on the map: choose one" : `Map: ${name}` }, kids));
}

const ICONS = {
  signal: () => svg("svg", { viewBox: "0 0 16 12", "aria-hidden": "true" }, [0, 1, 2, 3].map((i) => svg("rect", { x: String(i * 4), y: String(9 - i * 3), width: "3", height: String(3 + i * 3), rx: "0.5", fill: "currentColor" }))),
  battery: () => svg("svg", { viewBox: "0 0 24 12", "aria-hidden": "true" }, svg("rect", { x: "0.5", y: "0.5", width: "20", height: "11", rx: "3", fill: "none", stroke: "currentColor" }),
    svg("rect", { x: "2.5", y: "2.5", width: "14", height: "7", rx: "1.5", fill: "currentColor" }), svg("rect", { x: "21.5", y: "4", width: "2", height: "4", rx: "1", fill: "currentColor" })),
};

// ---------- The smartphone ----------

export class SmartPhone {
  // kind: caller, vht or transporter. ctx: as the basic phone's, and board(),
  // map(), now(), timeoutMs(), ussd(phone, body, keys) (shown on this
  // widget), progress(body), gps (get/set), report (get/set).
  constructor(mount, kind, ctx) {
    this.kind = kind;
    this.ctx = ctx;
    this.p = null;
    this.mode = "idle"; // idle | con | end, as the basic phone
    this.session = "";
    this.screen = "";
    this.busy = false;
    this.gen = 0;
    this.answered = 0; // screens answered in this session
    this.tapped = ""; // the key a scenario is pressing, highlighted
    this.flow = ""; // a multi-key action under way: what it says
    this.toast = null; // {text, bad, until}
    this.forceList = false; // the caller chose another place than the GPS one
    this.autoLandmark = ""; // after "use this location": answer the landmark screen too
    this.autoSent = "";
    this.picking = false; // the caller is moving their location
    this.facility = ""; // the transporter's chosen facility
    this.job = null; // the responder's open job, to say when it ends
    this.duty = true; // responders are seeded available
    this.alerted = new Set(); // phone|alert pairs already announced
    this.asked = new Set(); // arrival questions already announced
    this.lastKey = "";
    this.bannerTimer = 0;
    this.lock = h("p", { class: "phone-lock", hidden: true });
    this.clockEl = h("span", { class: "num", text: "" });
    this.bannerEl = h("button", { type: "button", class: "sp-banner", hidden: true });
    this.body = h("div", { class: "sp-body" });
    this.live = h("p", { class: "visually-hidden", "aria-live": "polite" });
    this.root = h("article", { class: "smart" },
      h("div", { class: "sp-screen" },
        h("div", { class: "sp-status", "aria-hidden": "true" }, this.clockEl, h("span", { class: "sp-notch" }),
          h("span", { class: "sp-sys" }, ICONS.signal(), ICONS.battery())),
        this.bannerEl, this.body, this.live, h("div", { class: "sp-homebar", "aria-hidden": "true" })));
    this.inbox = new Inbox(ctx);
    mount.replaceChildren(this.lock, this.root, this.inbox.root);
    this.render(true);
  }

  code() { return this.kind === "caller" ? this.ctx.codes().caller : this.ctx.codes().responder; }
  locked() { return this.p ? this.ctx.locked(this.p.id) : undefined; }

  setPhone(p) {
    if (this.p && p && this.p.id === p.id) return;
    this.p = p;
    this.gen++;
    this.mode = "idle";
    this.session = "";
    this.answered = 0;
    this.flow = "";
    this.toast = null;
    this.forceList = false;
    this.autoLandmark = "";
    this.picking = false;
    this.facility = "";
    this.hideBanner();
    this.inbox.reset();
    this.render(true);
    this.renderInbox();
  }

  end() {
    if (this.locked()) return;
    this.gen++;
    this.mode = "idle";
    this.session = "";
    this.answered = 0;
    this.forceList = false;
    this.autoLandmark = "";
    this.render(true);
  }

  async dial() {
    if (!this.p || this.busy) return;
    await this.send({ phone: this.p.id, code: this.code() });
  }

  async reply(key) {
    if (this.busy || this.mode !== "con") return;
    await this.send({ session: this.session, input: key });
  }

  async send(body) {
    const gen = this.gen;
    this.busy = true;
    this.render(true);
    const { status, data } = await this.ctx.api("POST", "api/ussd", body);
    this.busy = false;
    if (gen !== this.gen) { this.render(true); return; }
    this.show(status, data);
  }

  // show takes a USSD answer, from a tap here or a key a scenario sent.
  show(status, data) {
    if (status === 200 || status === 410) {
      const was = this.mode;
      this.mode = data.end ? "end" : "con";
      this.answered = was === "con" ? this.answered + 1 : 0;
      this.session = data.session || "";
      this.screen = data.text || "";
      if (data.end) {
        this.ctx.ended?.(this.p, this.screen);
        if (this.kind !== "caller") {
          // A responder's answer ends the menu: say what it said, and go back
          // to the app's own view of their alert or job.
          this.say(this.screen.split("\n")[0].replace(/^PENDING-[A-Z0-9]+ /, ""), false);
          this.mode = "idle";
        }
      }
    } else {
      this.say(data.error || "Something went wrong.", true);
    }
    this.render(true);
  }

  say(text, bad) {
    this.toast = { text, bad, until: Date.now() + 9000 };
    this.live.textContent = text;
  }

  // type shows a scenario's key as a tap on the matching button.
  async type(keys, signal) {
    const gen = this.gen;
    await sleep(READ_MS / 2, signal);
    if (gen !== this.gen) return;
    this.tapped = this.mode === "idle" ? "dial" : keys;
    this.render(true);
    await sleep(READ_MS / 2 + 150, signal);
    if (gen === this.gen) this.tapped = "";
  }

  // flowRun runs one action of several keys from this phone's menu, shown
  // on the widget as a scenario's would be. check(screen) may stop it after
  // the first screen with a message.
  async flowRun(label, keys, check) {
    if (!this.p || this.busy || this.flow) return null;
    this.flow = label;
    this.render(true);
    try {
      let r = await this.ctx.ussd(this.p.id, { phone: this.p.id, code: this.code() }, this.code());
      if (r.status === 200 && !r.data.end && check) {
        const why = check(r.data.text || "");
        if (why) { this.say(why, true); this.mode = "idle"; return r; }
      }
      for (const k of keys) {
        if (r.status !== 200 || r.data.end) break;
        r = await this.ctx.ussd(this.p.id, { session: r.data.session, input: k }, k);
      }
      return r;
    } finally {
      this.flow = "";
      this.render(true);
    }
  }

  // ---------- Notifications ----------

  showBanner(title, text) {
    this.bannerEl.replaceChildren(h("span", { class: "sp-icon", "aria-hidden": "true", text: "S" }), h("b", { text: title }), h("span", { text }));
    this.bannerEl.hidden = false;
    this.bannerEl.onclick = () => this.hideBanner();
    this.live.textContent = `${title}: ${text}`;
    clearTimeout(this.bannerTimer);
    this.bannerTimer = setTimeout(() => this.hideBanner(), 7000);
  }

  hideBanner() {
    clearTimeout(this.bannerTimer);
    this.bannerEl.hidden = true;
  }

  renderInbox() {
    const fresh = this.inbox.render(this.p);
    if (fresh.length) this.showBanner("Sauti · SMS", fresh[fresh.length - 1].text);
  }

  // refresh re-renders when the board, the clock or the inbox changed what
  // this phone shows (a poll or the second's tick calls it).
  refresh() {
    if (this.p && this.kind === "caller") {
      const ask = this.ctx.askFor(this.p.id);
      if (ask && !this.asked.has(ask.session)) {
        this.asked.add(ask.session);
        this.showBanner("Sauti", `Has a health worker arrived yet? Incident ${ask.ref}.`);
      }
    }
    if (this.p && this.kind !== "caller") {
      const st = this.status();
      // A job that ends while on screen (the handover, or the coordinator's
      // close) is said, not just dropped.
      if (this.job && this.job.phone === this.p.id && !(st.kind === "busy" && st.inc.id === this.job.id)) {
        const done = (this.ctx.board()?.incidents || []).find((i) => i.id === this.job.id);
        if (done && (done.state === "closed" || done.state === "at_facility")) this.say(`Incident ${done.reference} is closed. Thank you.`, false);
        this.job = null;
      }
      if (st.kind === "busy") this.job = { phone: this.p.id, id: st.inc.id };
      if (st.kind === "alert") {
        const key = this.p.id + "|" + st.alert.id;
        if (!this.alerted.has(key)) {
          this.alerted.add(key);
          const sev = SEV[st.inc.severity || "none"];
          this.showBanner(`${sev} emergency`, `${st.inc.category || "Emergency"} at ${place(st.inc)}. Tap the alert to answer.`);
        }
      }
    }
    this.render(false);
  }

  status() {
    return responderStatus(this.ctx.board(), this.p.label, this.ctx.timeoutMs());
  }

  // ---------- Rendering ----------

  render(force) {
    const now = this.ctx.now();
    this.clockEl.textContent = new Date(now).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    const lock = this.locked();
    this.lock.textContent = lock || "";
    this.lock.hidden = !lock;
    this.root.classList.toggle("driven", Boolean(lock));
    this.root.setAttribute("aria-label", this.p ? `${this.p.label}'s smartphone` : "Smartphone");
    if (this.toast && this.toast.until < Date.now()) this.toast = null;
    const vm = this.p ? (this.kind === "caller" ? this.callerModel(now) : this.responderModel(now)) : { view: "off" };
    Object.assign(vm, { busy: this.busy, flow: this.flow, tapped: this.tapped, lock: Boolean(lock), live: this.ctx.live(), toast: this.toast });
    const key = JSON.stringify(vm);
    if (!force && key === this.lastKey) return;
    this.lastKey = key;
    const focused = this.body.contains(document.activeElement) ? document.activeElement.dataset.focus : "";
    this.body.replaceChildren(...this.build(vm).flat(Infinity).filter(Boolean));
    sizeBars(this.body);
    if (focused) this.body.querySelector(`[data-focus="${CSS.escape(focused)}"]`)?.focus();
  }

  off(vm) { return vm.busy || vm.lock || !vm.live || Boolean(vm.flow); }

  btn(vm, label, onclick, { cls = "", key = "", focus = "" } = {}) {
    return h("button", { type: "button", class: "sp-btn " + cls + (vm.tapped && key && vm.tapped === key ? " tapped" : ""),
      disabled: this.off(vm), "data-key": key || null, "data-focus": focus || label, onclick, text: label });
  }

  header(sub) {
    return h("div", { class: "sp-app" }, h("span", { class: "sp-app-name" }, h("span", { class: "sp-icon", "aria-hidden": "true", text: "S" }), "Sauti"),
      h("span", { class: "sp-who" }, this.p.label, h("br"), sub || ""));
  }

  build(vm) {
    if (vm.view === "off") return [h("p", { class: "sp-sub", text: "Waiting for the demo…" })];
    const out = [this.header(vm.sub)];
    if (vm.toast) out.push(h("p", { class: "sp-toast" + (vm.toast.bad ? " bad" : ""), role: "status", text: vm.toast.text }));
    if (vm.flow) out.push(h("p", { class: "sp-toast", text: vm.flow }));
    const views = {
      "caller-home": () => this.callerHome(vm), question: () => this.question(vm), location: () => this.locationView(vm),
      confirming: () => [h("p", { class: "sp-title", text: "Using your location" }), h("p", { class: "sp-sub", text: `Sending ${vm.near} as the nearest place…` })],
      done: () => this.doneView(vm), "resp-home": () => this.respHome(vm), alert: () => this.alertView(vm), menu: () => this.question(vm),
      job: () => this.jobView(vm), dialing: () => [h("p", { class: "sp-title", text: "Opening Sauti…" }), h("p", { class: "sp-sub", text: "Connecting to the Sauti service." })],
    };
    out.push(views[vm.view]());
    return out;
  }

  // ---------- The caller's app ----------

  gps() {
    const map = this.ctx.map();
    let g = this.ctx.gps.get(this.p.id);
    if (!g && map && this.kind !== "caller") g = homeFix(this.p, this.ctx.phones(), map);
    if (!g && map) {
      const home = map.landmarks.find((l) => l.id === GPS_HOME[this.p.id]) || map.landmarks[0];
      if (home) g = { lat: home.lat + 0.0002, lon: home.lon + 0.0002 };
    }
    return g || null;
  }

  // nearestPlaces: the ward whose centre is nearest the fix, and its nearest
  // landmark (or, given choices, the nearest landmark the screen offers).
  nearestPlaces(g, offered) {
    const map = this.ctx.map();
    if (!g || !map) return { ward: null, landmark: null };
    const ward = nearest(g, map.wards);
    const pool = offered ? map.landmarks.filter((l) => offered.includes(l.id)) : map.landmarks.filter((l) => ward && l.ward === ward.id);
    return { ward, landmark: nearest(g, pool) };
  }

  callerModel(now) {
    const map = this.ctx.map();
    const g = this.gps();
    const near = this.nearestPlaces(g);
    const ref = this.ctx.report.get(this.p.id);
    const inc = ref ? (this.ctx.board()?.incidents || []).find((i) => i.reference === ref) : null;
    const tracker = inc ? this.trackerOf(inc) : null;
    const ask = this.ctx.askFor(this.p.id);
    const base = { sub: "Emergency help", gps: g, near: near.landmark?.name || "", ward: near.ward?.name || "", nearId: near.landmark?.id || "",
      ask: ask ? { incident: ask.incident, ref: ask.ref, session: ask.session } : null };
    if (this.mode === "idle") {
      return { ...base, view: this.tapped === "dial" ? "dialing" : "caller-home", picking: this.picking, ref, tracker, state: inc?.state || "" };
    }
    if (this.mode === "end") {
      const done = (REF.exec(this.screen) || [""])[0];
      return { ...base, view: "done", text: this.screen, ref: done, tracker: done && inc && inc.reference === done ? tracker : null, state: inc?.state || "" };
    }
    const s = parseScreen(this.screen);
    const pk = placeKind(s.options, map);
    const driven = Boolean(this.locked());
    if (pk.kind === "ward" && !this.forceList && !driven && near.ward) {
      const key = Object.keys(pk.ids).find((k) => pk.ids[k] === near.ward.id);
      if (key) return { ...base, view: "location", wardKey: key, progress: progressOf(this.answered) };
    }
    if (pk.kind === "landmark" && this.autoLandmark && !driven) {
      const offered = Object.values(pk.ids);
      const lm = this.nearestPlaces(g, offered).landmark;
      const key = lm && Object.keys(pk.ids).find((k) => pk.ids[k] === lm.id);
      if (key) {
        if (this.autoSent !== this.session + "|" + this.screen) {
          this.autoSent = this.session + "|" + this.screen;
          setTimeout(() => { this.autoLandmark = ""; this.reply(key); }, 700);
        }
        return { ...base, view: "confirming", near: lm.name };
      }
    }
    return { ...base, view: "question", prompt: s.prompt, options: s.options, after: s.after, progress: progressOf(this.answered),
      place: pk.kind };
  }

  // trackerOf is the caller's tracker, naming the facility the patient was
  // taken to once the transporter hands over there, so the family can follow.
  trackerOf(inc) {
    const t = callerProgress(inc);
    const at = inc.events.find((e) => e.type === "responder.at_facility");
    const id = at ? payload(at).facility : "";
    const name = (this.ctx.map()?.facilities || []).find((f) => f.id === id)?.name;
    if (name) t.steps = t.steps.map((s) => (s.key === "facility" ? { ...s, label: `At ${name}` } : s));
    // The caller's own "yes" counts as help arrived, even before a responder records it.
    const check = (this.ctx.board()?.arrival || []).find((a) => a.incident === inc.id);
    if (check?.confirmedAt) {
      t.steps = t.steps.map((s) => (s.key === "arrived" && !s.done ? { ...s, done: true, label: "Help has arrived (you said so)" } : s));
      t.current = (t.steps.find((s) => !s.done) || {}).key || "";
    }
    return t;
  }

  // askCard is the arrival question in the caller's app: the same question
  // a basic phone gets as a USSD push, answered with a tap.
  askCard(vm) {
    if (!vm.ask) return null;
    return h("section", { class: "sp-card ask", role: "group", "aria-label": "Question from Sauti" },
      h("h3", { text: "Has a health worker arrived yet?" }),
      h("p", { class: "sp-small", text: `Incident ${vm.ask.ref}. Sauti asks every few minutes until help arrives, so it knows when help came.` }),
      h("div", { class: "sp-actions two" },
        this.btn(vm, "Yes, help is here", () => this.answer(vm.ask, true), { focus: "ask-yes" }),
        this.btn(vm, "No, still waiting", () => this.answer(vm.ask, false), { cls: "alt", focus: "ask-no" })));
  }

  async answer(ask, yes) {
    if (this.busy) return;
    this.busy = true;
    this.render(true);
    const { status, data } = await this.ctx.arrival({ phone: this.p.id, incident: ask.incident, answer: yes ? "yes" : "no" });
    this.busy = false;
    this.say(status === 200 ? data.text.replace(/^PENDING-[A-Z0-9-]+ /, "") : data.error || "The answer did not reach Sauti.", status !== 200);
    this.render(true);
  }

  callerHome(vm) {
    const map = this.ctx.map();
    const out = [
      this.askCard(vm),
      h("p", { class: "sp-title", text: "Need emergency help?" }),
      h("p", { class: "sp-sub", text: "Sauti finds the nearest trained health worker and transport. The questions take about a minute." }),
      this.btn(vm, "Report an emergency", () => this.dial(), { cls: "sos", key: "dial" }),
      h("section", { class: "sp-card" },
        h("h3", { text: "Your location" }),
        h("p", { class: "sp-text", text: vm.near ? `Near ${vm.near}, ${vm.ward}` : "Finding your location…" }),
        h("p", { class: "sp-small", text: "From this phone's GPS (simulated in the demo). Sauti uses it to answer the ward and place questions for you." }),
        vm.picking ? miniMap(map, { me: vm.gps, pick: vm.nearId, onPick: (l) => this.moveTo(l) }) : miniMap(map, { me: vm.gps, pick: vm.nearId }),
        h("button", { type: "button", class: "sp-link", "data-focus": "move", disabled: this.off(vm),
          text: vm.picking ? "Done" : "Move my location", onclick: () => { this.picking = !this.picking; this.render(true); } }),
        vm.picking ? h("p", { class: "sp-small", text: "Choose a place on the map: your location moves next to it." }) : null),
    ];
    if (vm.tracker) out.push(this.trackerCard(vm.ref, vm.tracker, vm.state));
    return out;
  }

  moveTo(l) {
    const g = { lat: l.lat + 0.0002, lon: l.lon + 0.0002 };
    this.ctx.gps.set(this.p.id, g);
    this.picking = false;
    this.render(true);
    if (this.kind === "vht") this.reshare(g);
  }

  // reshare sends a VHT's new location to their transporter, while on a job.
  async reshare(g) {
    const st = this.status();
    if (st.kind !== "busy") return;
    const { status, data } = await this.ctx.share({ phone: this.p.id, incident: st.inc.id, facility: this.p.facility, lat: g.lat, lon: g.lon });
    this.say(status === 200 ? (data.transporter ? `Your new location was sent to ${data.transporter}.` : "Saved: the transporter gets it when one accepts.")
      : data.error || "Your location could not be sent.", status !== 200);
    this.render(true);
  }

  trackerCard(ref, t, state) {
    const steps = t.steps.map((s) => h("li", { class: s.done ? "done" : s.key === t.current ? "now" : "" }, h("span", { class: "dot", "aria-hidden": "true" }),
      h("span", { text: s.label + (s.done ? "" : s.key === t.current ? " (now)" : "") })));
    return h("section", { class: "sp-card" + (state === "closed" ? " ok" : "") },
      h("h3", { text: `Your report ${ref}` }),
      t.merged ? h("p", { class: "sp-text", text: "Another call reported the same emergency: help is being sent to that one." }) : null,
      t.escalated ? h("p", { class: "sp-text", text: "A coordinator is arranging help for you." }) : null,
      h("ol", { class: "sp-steps", "aria-label": "Progress" }, steps));
  }

  locationView(vm) {
    const map = this.ctx.map();
    return [
      h("div", { class: "sp-progress", "aria-hidden": "true" }, h("span", { "data-w": vm.progress })),
      h("p", { class: "sp-title", text: "Where is the emergency?" }),
      miniMap(map, { me: vm.gps, pick: vm.nearId }),
      h("p", { class: "sp-text", text: `Near ${vm.near}, ${vm.ward}` }),
      h("p", { class: "sp-small", text: "Found from this phone's location. Sauti answers the ward and the nearest place for you." }),
      h("div", { class: "sp-actions" },
        this.btn(vm, "Use this location", () => { this.autoLandmark = vm.nearId; this.reply(vm.wardKey); }, { key: vm.wardKey }),
        this.btn(vm, "Choose another place", () => { this.forceList = true; this.render(true); }, { cls: "alt" }),
        h("button", { type: "button", class: "sp-link", text: "Cancel the report", disabled: this.off(vm), onclick: () => this.end() })),
    ];
  }

  question(vm) {
    const opts = vm.options.map((o) => h("button", { type: "button", class: "sp-opt" + (vm.tapped === o.key ? " tapped" : ""), "data-key": o.key,
      "data-focus": "opt-" + o.key, disabled: this.off(vm), onclick: () => this.reply(o.key), text: o.label }));
    let input = null;
    if (!vm.options.length) {
      const box = h("input", { class: "screen-input", "aria-label": "Your answer", maxlength: "64", disabled: this.off(vm), "data-focus": "answer",
        onkeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); this.reply(box.value.trim()); } } });
      input = [box, this.btn(vm, "Send", () => this.reply(box.value.trim()))];
    }
    return [
      this.kind === "caller" ? h("div", { class: "sp-progress", "aria-hidden": "true" }, h("span", { "data-w": vm.progress })) : null,
      h("p", { class: "sp-text", text: vm.prompt }),
      h("div", { class: "sp-opts" }, opts), input,
      vm.after ? h("p", { class: "sp-small", text: vm.after }) : null,
      h("button", { type: "button", class: "sp-link", text: this.kind === "caller" ? "Cancel the report" : "Close", disabled: this.off(vm), onclick: () => this.end() }),
    ];
  }

  doneView(vm) {
    const out = [this.askCard(vm)];
    if (vm.ref) {
      out.push(h("p", { class: "sp-title", text: "Help is coming" }), h("p", { class: "sp-sub num", text: `Incident ${vm.ref}` }));
      out.push(h("section", { class: "sp-card" }, h("h3", { text: "What Sauti said" }), h("p", { class: "sp-text", text: vm.text })));
      if (vm.tracker) out.push(this.trackerCard(vm.ref, vm.tracker, vm.state));
      out.push(h("p", { class: "sp-small", text: "Keep this phone near you: Sauti sends updates by SMS." }));
    } else {
      out.push(h("p", { class: "sp-title", text: "Call ended" }), h("p", { class: "sp-text", text: vm.text }));
    }
    out.push(h("div", { class: "sp-actions" }, this.btn(vm, "Done", () => this.end(), { cls: "alt" })));
    return out;
  }

  // ---------- The responder's app ----------

  stage() {
    const map = this.ctx.map();
    return (map?.stages || []).find((s) => s.id === this.p.stage) || null;
  }

  placeOf(inc) {
    const map = this.ctx.map();
    if (!map || !inc) return null;
    const lm = map.landmarks.find((l) => l.id === inc.landmarkId);
    if (lm) return { lat: lm.lat, lon: lm.lon, label: lm.name };
    const w = map.wards.find((x) => x.id === inc.wardId);
    return w ? { lat: w.lat, lon: w.lon, label: w.name } : null;
  }

  responderModel(now) {
    const role = this.p.role;
    const st = this.status();
    const me = this.gps();
    const sub = `${role === "vht" ? "VHT" : "Transporter"} · ${this.p.ward || ""}`;
    if (this.mode === "con") {
      // The menu is open (a scenario, the autopilot or a tap here): an alert
      // it offers reads as the alert card, with its keys on the buttons.
      const s = parseScreen(this.screen);
      if (s.options.some((o) => o.label === "Accept") && st.kind === "alert") return { ...this.alertModel(st, me, now), sub, menu: true };
      return { view: "menu", sub, prompt: s.prompt, options: s.options, after: s.after };
    }
    if (this.tapped === "dial" && !this.flow) return { view: "dialing", sub };
    if (st.kind === "busy") return { ...this.jobModel(st, me), sub };
    if (st.kind === "alert") return { ...this.alertModel(st, me, now), sub, menu: false };
    const near = this.nearestPlaces(me);
    const map = this.ctx.map();
    return { view: "resp-home", sub, duty: this.duty, stage: this.stage()?.name || "", gps: me, near: near.landmark?.name || "", ward: near.ward?.name || "",
      nearId: near.landmark?.id || "", picking: this.picking, number: this.p.number || "",
      facility: (map?.facilities || []).find((f) => f.id === this.p.facility)?.name || "",
      recent: st.recent.slice(0, 4).map(({ inc, alert }) => ({ ref: inc.reference, outcome: alert.outcome, at: clock(new Date(alert.sentAt)) })) };
  }

  alertModel(st, me, now) {
    const dest = this.placeOf(st.inc);
    const t = me && dest ? trip(me, dest, this.p.role) : null;
    return { view: "alert", ref: st.inc.reference, sev: st.inc.severity || "none", category: st.inc.category, place: place(st.inc),
      late: st.late, left: st.deadline ? Math.max(0, Math.round((st.deadline - now) / 1000)) : null, km: t?.km ?? null, min: t?.min ?? null,
      me: me ? { lat: me.lat, lon: me.lon } : null, dest };
  }

  jobModel(st, me) {
    const inc = st.inc;
    const role = this.p.role;
    const step = nextStep(inc, role);
    const scene = this.placeOf(inc);
    const arrived = arrivedRoles(inc).has(role);
    const map = this.ctx.map();
    const facilities = (map?.facilities || []).map((f) => ({ ...f, km: scene ? Math.round(distanceM(scene, f) / 100) / 10 : null }))
      .sort((a, b) => (a.km ?? 0) - (b.km ?? 0));
    const fac = facilities.find((f) => f.id === this.facility) || facilities[0] || null;
    const toFacility = role === "transporter" && Boolean(inc.t.T6);
    const from = toFacility || arrived ? scene : me;
    const to = toFacility ? fac && { lat: fac.lat, lon: fac.lon, label: fac.name } : scene;
    const t = from && to && !(arrived && !toFacility) ? trip(from, to, role) : null;
    const crew = this.crewOf(inc, role, me, scene, !arrived && !toFacility);
    return { view: "job", ref: inc.reference, incId: inc.id, sev: inc.severity || "none", category: inc.category, place: place(inc), state: inc.state,
      step, arrived, toFacility, facilities: step === "at_facility" ? facilities.map((f) => ({ id: f.id, name: f.name, km: f.km })) : [],
      facility: fac?.id || "", partner: partner(inc, role), km: t?.km ?? null, min: t?.min ?? null, ...crew,
      me: from ? { lat: from.lat, lon: from.lon } : null, dest: toFacility ? null : scene, fac: toFacility && to ? to : null };
  }

  // crewOf is what a responder knows of the other one on the job. A
  // transporter: where the VHT is (once shared), the facility they work at
  // and their number, and the trip straight to the patient against picking
  // the VHT up first. A VHT: the transporter coming and their number, and
  // whether their own details have gone.
  crewOf(inc, role, me, scene, enRoute) {
    const share = (this.ctx.board()?.shares || []).find((s) => s.incident === inc.id);
    const other = inc.alerts.find((a) => a.role === (role === "vht" ? "transporter" : "vht") && a.outcome === "accepted");
    if (role === "transporter") {
      if (!other) return { vht: null };
      const v = { label: other.responder, shared: Boolean(share && share.vht === other.responder) };
      if (!v.shared) return { vht: v };
      Object.assign(v, { place: [share.landmark, share.ward].filter(Boolean).join(", "), facility: share.facility, number: share.number,
        km: share.km ?? null, from: share.via });
      if (me && scene && enRoute) {
        const pt = { lat: share.lat, lon: share.lon };
        const direct = trip(me, scene, role);
        const a = trip(me, pt, role);
        const b = trip(pt, scene, role);
        Object.assign(v, { direct, first: { km: Math.round((a.km + b.km) * 10) / 10, min: a.min + b.min } });
      }
      return { vht: v, vhtPt: enRoute ? { lat: share.lat, lon: share.lon, label: other.responder } : null };
    }
    const tp = other && this.ctx.phones().find((p) => p.label === other.responder);
    const mine = share && share.vht === this.p.label ? share : null;
    return { crew: { transporter: other?.responder || "", number: tp?.number || "",
      shared: mine ? { place: [mine.landmark, mine.ward].filter(Boolean).join(", "), facility: mine.facility, told: mine.told } : null } };
  }

  respHome(vm) {
    const map = this.ctx.map();
    return [
      h("p", { class: "sp-title", text: vm.duty ? "You're on duty" : "You're off duty" }),
      h("p", { class: "sp-sub", text: vm.duty ? "Emergency alerts near you appear here as soon as Sauti sends them." : "Sauti will not alert you until you go on duty." }),
      h("div", { class: "sp-switch" }, h("span", { text: "Available for alerts" }),
        h("button", { type: "button", "aria-pressed": String(vm.duty), disabled: this.off(vm), "data-focus": "duty", text: vm.duty ? "On duty" : "Off duty",
          onclick: () => this.setDuty(!vm.duty) })),
      h("section", { class: "sp-card" }, h("h3", { text: "Your location" }),
        h("p", { class: "sp-text", text: vm.near ? `Near ${vm.near}, ${vm.ward}` : "Finding your location…" }),
        h("p", { class: "sp-small", text: "From this phone's GPS (simulated in the demo, starting by your registered stage)." }),
        vm.picking ? miniMap(map, { me: vm.gps, pick: vm.nearId, onPick: (l) => this.moveTo(l) }) : miniMap(map, { me: vm.gps, pick: vm.nearId }),
        h("button", { type: "button", class: "sp-link", "data-focus": "move", disabled: this.off(vm),
          text: vm.picking ? "Done" : "Move my location", onclick: () => { this.picking = !this.picking; this.render(true); } }),
        vm.picking ? h("p", { class: "sp-small", text: "Choose a place on the map: your location moves next to it." }) : null),
      h("section", { class: "sp-card" }, h("h3", { text: "Registered with Sauti" }),
        vm.facility ? h("div", { class: "sp-row" }, h("span", { text: "Health facility" }), h("b", { text: vm.facility })) : null,
        h("div", { class: "sp-row" }, h("span", { text: "Stage" }), h("b", { text: vm.stage || "None" })),
        h("div", { class: "sp-row" }, h("span", { text: "Number" }), h("b", { class: "num", text: vm.number })),
        h("p", { class: "sp-small", text: this.kind === "vht"
          ? "When you accept, the app sends your facility, your number and your location to the transporter."
          : "When a VHT is on your job too, Sauti sends you where they are, their facility and their number." })),
      h("section", { class: "sp-card" }, h("h3", { text: "Recent alerts" }),
        vm.recent.length ? h("ul", { class: "sp-recent" }, vm.recent.map((r) => h("li", {}, h("span", { class: "num", text: `${r.ref} · ${r.at}` }),
          h("span", { class: "tone-" + OUTCOME_TONE[r.outcome], text: OUTCOME[r.outcome] || r.outcome })))) : h("p", { class: "sp-small", text: "None yet." })),
    ];
  }

  alertView(vm) {
    const map = this.ctx.map();
    const timing = vm.late
      ? h("p", { class: "sp-text tone-declined", text: "The answer window has passed, but nobody has taken this yet. You can still accept." })
      : vm.left !== null ? h("p", { class: "sp-text" }, "Answer within ", h("b", { class: "num", text: mmss(vm.left * 1000) })) : null;
    const accept = () => (vm.menu ? this.reply("1") : this.accept(vm.ref));
    const decline = () => (vm.menu ? this.reply("2") : this.decline(vm.ref));
    return [
      h("p", { class: "sp-title" }, h("span", { class: "sev-dot " + vm.sev, "aria-hidden": "true" }), h("span", { class: "sev " + vm.sev, text: `${SEV[vm.sev]} emergency` })),
      h("section", { class: "sp-card alert" },
        h("p", { class: "sp-text", text: vm.category || "Emergency" }),
        h("div", { class: "sp-row" }, h("span", { text: "Where" }), h("b", { text: vm.place })),
        vm.km !== null ? h("div", { class: "sp-row" }, h("span", { text: "From you" }), h("b", { text: `${vm.km} km · about ${vm.min} min` })) : null,
        h("div", { class: "sp-row" }, h("span", { text: "Incident" }), h("b", { class: "num", text: vm.ref }))),
      timing,
      miniMap(map, { me: vm.me, dest: vm.dest, route: [vm.me, vm.dest] }),
      h("p", { class: "sp-small", text: "Distance and time are estimates from your location." }),
      h("div", { class: "sp-actions" },
        this.btn(vm, "Accept", accept, { key: "1", focus: "accept" }),
        this.btn(vm, "Decline", decline, { cls: "alt", key: "2", focus: "decline" })),
    ];
  }

  jobView(vm) {
    const map = this.ctx.map();
    const status = vm.toFacility ? (vm.state === "transporting" ? "Taking the patient to the facility" : "At the facility")
      : vm.arrived ? "You're on scene" : "Go to the patient";
    const out = [
      h("p", { class: "sp-title", text: status }),
      h("p", { class: "sp-sub" }, h("span", { class: "sev " + vm.sev, text: SEV[vm.sev] }), h("span", { class: "num", text: `  ·  ${vm.ref}` })),
      h("section", { class: "sp-card" },
        h("div", { class: "sp-row" }, h("span", { text: "What" }), h("b", { text: vm.category || "Emergency" })),
        h("div", { class: "sp-row" }, h("span", { text: "Where" }), h("b", { text: vm.place })),
        vm.km !== null ? h("div", { class: "sp-row" }, h("span", { text: vm.toFacility ? "To the facility" : "From you" }), h("b", { text: `${vm.km} km · about ${vm.min} min` })) : null,
        h("div", { class: "sp-row" }, h("span", { text: "Caller" }), h("b", { text: "Sauti bridge line" })),
        h("p", { class: "sp-small", text: "The caller's number is never shown. Calling through the bridge is not part of the demo." }),
        h("p", { class: "sp-text tone-" + vm.partner.tone, text: vm.partner.text })),
      this.vhtCard(vm), this.crewCard(vm),
      miniMap(map, { me: vm.me, dest: vm.dest, fac: vm.fac, vht: vm.vhtPt, route: [vm.me, vm.dest || vm.fac] }),
    ];
    if (vm.step === "at_facility" && vm.facilities.length) {
      out.push(h("fieldset", { class: "sp-fac" }, h("legend", { class: "sp-small", text: "Which facility?" }),
        vm.facilities.map((f) => h("label", {}, h("input", { type: "radio", name: "fac-" + this.p.id, value: f.id, checked: f.id === vm.facility, disabled: this.off(vm),
          "data-focus": "fac-" + f.id, onchange: () => { this.facility = f.id; this.render(true); } }), f.name, h("small", { text: f.km !== null ? `${f.km} km` : "" })))));
    }
    if (vm.step) {
      out.push(h("div", { class: "sp-actions" }, this.btn(vm, STEP_LABEL[vm.step], () => this.record(vm.incId, vm.step, vm.facility), { focus: "step" }),
        h("p", { class: "sp-small", text: "Recording this step is a preview of milestone M3." })));
    } else if (!vm.toFacility) {
      out.push(h("p", { class: "sp-small", text: vm.arrived ? "Stay with the patient. The transporter records the trip to the facility." : "The patient has already left for the facility." }));
    }
    return out;
  }

  // vhtCard: on a transporter's job, the VHT's place, facility and number.
  vhtCard(vm) {
    const v = vm.vht;
    if (!v) return null;
    if (!v.shared) {
      return h("section", { class: "sp-card" }, h("h3", { text: `${v.label}, the VHT` }),
        h("p", { class: "sp-text", text: `${v.label} is coming too. Their place has not reached you yet.` }));
    }
    return h("section", { class: "sp-card" }, h("h3", { text: `${v.label}, the VHT` }),
      h("div", { class: "sp-row" }, h("span", { text: "Where" }), h("b", { text: v.place })),
      v.km !== null ? h("div", { class: "sp-row" }, h("span", { text: "From the patient" }), h("b", { text: `${v.km} km` })) : null,
      h("div", { class: "sp-row" }, h("span", { text: "Works at" }), h("b", { text: v.facility })),
      h("div", { class: "sp-row" }, h("span", { text: "Call" }), h("b", { class: "num", text: v.number })),
      v.direct ? h("p", { class: "sp-text" }, `Straight to the patient: ${v.direct.km} km, about ${v.direct.min} min.`,
        h("br"), `Picking up ${v.label} first: ${v.first.km} km, about ${v.first.min} min.`) : null,
      h("p", { class: "sp-small", text: `Sent by ${v.label}${v.from === "app" ? "'s smartphone" : " on " + this.ctx.codes().incident}. Numbers show masked in the demo.` }));
  }

  // crewCard: on a VHT's job, the transporter coming and what was sent.
  crewCard(vm) {
    const c = vm.crew;
    if (!c) return null;
    return h("section", { class: "sp-card" }, h("h3", { text: "Your transporter" }),
      c.transporter ? [h("div", { class: "sp-row" }, h("span", { text: "Who" }), h("b", { text: c.transporter })),
        h("div", { class: "sp-row" }, h("span", { text: "Call" }), h("b", { class: "num", text: c.number }))]
        : h("p", { class: "sp-text", text: "No transporter has accepted yet." }),
      h("p", { class: "sp-small", text: c.shared
        ? `${c.shared.told ? "Sent to the transporter" : "Ready for the transporter who accepts"}: you are near ${c.shared.place}, you work at ${c.shared.facility}, and your number. From your registration and this phone's location.`
        : "Sending your location, facility and number…" }));
  }

  // ---------- Responder actions ----------

  async accept(ref) {
    const r = await this.flowRun("Accepting…", ["1"], (text) => (text.includes(ref) && text.includes("1 Accept") ? "" : "The menu offered a different alert: try again in a moment."));
    if (r && r.status !== 200) this.say(r.data.error || "The answer did not reach Sauti.", true);
  }

  async decline(ref) {
    // Decline may ask why: the reasons screen stays open for the responder to choose.
    const r = await this.flowRun("Declining…", ["2"], (text) => (text.includes(ref) && text.includes("2 Decline") ? "" : "The menu offered a different alert: try again in a moment."));
    if (r && r.status !== 200) this.say(r.data.error || "The answer did not reach Sauti.", true);
  }

  async setDuty(on) {
    const r = await this.flowRun(on ? "Going on duty…" : "Going off duty…", ["3", on ? "1" : "2"]);
    if (r && r.status === 200) this.duty = on;
    this.render(true);
  }

  async record(incident, step, facility) {
    if (this.busy) return;
    this.busy = true;
    this.render(true);
    const body = { phone: this.p.id, incident, step };
    if (step === "at_facility") body.facility = facility;
    const { status, data } = await this.ctx.progress(body);
    this.busy = false;
    this.say(status === 200 ? STEP_DONE[step] : data.error || "The step could not be recorded.", status !== 200);
    this.render(true);
  }
}

function place(inc) {
  return [inc.landmark && inc.landmark !== "other" ? inc.landmark : "", inc.ward].filter(Boolean).join(", ") || "Place not given yet";
}

// sizeBars sets each progress bar from its data-w: the page allows no
// inline style attribute, but setting the property from script is allowed.
function sizeBars(root) {
  for (const s of root.querySelectorAll(".sp-progress span[data-w]")) s.style.width = Math.round(Number(s.dataset.w) * 100) + "%";
}
