// A basic phone: a USSD screen, a keypad and an SMS inbox. Any handset can do
// this: USSD needs no data and no airtime. A scenario or the autopilot drives
// it through the same interface as the smartphone (smart.js): setPhone, type,
// show, render, renderInbox, locked, mode and gen.
import { h, clock } from "./dom.js";

export const READ_MS = 650; // the audience reads a screen before the next key
export const KEY_MS = 110;

// sleep resolves after ms, or rejects once signal is aborted.
export function sleep(ms, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(signal.reason); return; }
    const t = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => { clearTimeout(t); reject(signal.reason); }, { once: true });
  });
}

// Inbox is a phone's SMS list, newest first; the newest is marked when it
// arrived since the last render. Clear empties this phone's list on this
// page, as deleting messages on a handset does: the messages stay in the
// system's records, the figures and the activity feed.
export class Inbox {
  constructor(ctx) {
    this.ctx = ctx;
    this.p = null;
    this.msgs = h("ol", { class: "msgs" });
    this.empty = h("p", { class: "empty", text: "No messages yet." });
    this.count = h("span", { class: "inbox-count" });
    this.title = h("h3", { tabindex: "-1" }, "SMS inbox", this.count);
    this.clearBtn = h("button", { type: "button", class: "inbox-clear", text: "Clear", disabled: true, onclick: () => this.clear() });
    this.root = h("section", { class: "inbox", "aria-label": "SMS inbox" }, h("div", { class: "inbox-head" }, this.title, this.clearBtn), this.empty, this.msgs);
    this.last = undefined;
  }

  reset() { this.last = undefined; }

  clear() {
    if (!this.p) return;
    this.ctx.clear(this.p.id);
    this.render(this.p);
    this.title.focus(); // the button is now disabled: keep the reader's place
  }

  // render returns the messages that are new since the last render.
  render(p) {
    this.p = p || null;
    const all = (p && this.ctx.inbox(p.id)) || [];
    const list = all.slice(p ? Math.min(this.ctx.cleared(p.id), all.length) : 0);
    const fresh = this.last === undefined ? [] : all.slice(this.last);
    this.last = all.length;
    if (p) this.ctx.seen(p.id, all.length);
    this.empty.hidden = list.length > 0;
    this.empty.textContent = all.length ? "No messages. Cleared on this page." : "No messages yet.";
    this.count.textContent = list.length ? `(${list.length})` : "";
    this.clearBtn.disabled = !list.length;
    this.clearBtn.setAttribute("aria-label", p ? `Clear ${p.label}'s SMS inbox` : "Clear the SMS inbox");
    this.msgs.replaceChildren(...list.slice().reverse().map((m, i) =>
      h("li", { class: fresh.length && i === 0 ? "new" : "" }, h("time", { datetime: m.at, text: clock(new Date(m.at)) }), h("p", { text: m.text }))));
    return fresh;
  }
}

export class BasicPhone {
  // kind: caller, vht or transporter. ctx: api, live(), codes(), locked(id),
  // inbox(id), seen(id, n).
  constructor(mount, kind, ctx) {
    this.kind = kind;
    this.ctx = ctx;
    this.p = null;
    this.mode = "idle"; // idle | con | end
    this.session = "";
    this.screen = "";
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
    this.inbox = new Inbox(ctx);
    mount.replaceChildren(this.lock, this.root, this.inbox.root);
    this.render();
  }

  code() { return this.kind === "caller" ? this.ctx.codes().caller : this.ctx.codes().responder; }

  setPhone(p) {
    if (this.p && p && this.p.id === p.id) return;
    this.p = p;
    this.gen++;
    this.mode = "idle";
    this.session = "";
    this.input.value = ""; // one box serves every phone: keys typed for the last one are not this one's
    this.inbox.reset();
    this.render();
    this.renderInbox();
  }

  locked() { return this.p ? this.ctx.locked(this.p.id) : undefined; }

  press(k) {
    if (this.mode === "end" || this.locked()) return;
    this.input.value += k;
    this.input.focus();
  }

  primary() {
    if (this.busy || !this.p || !this.ctx.live() || this.locked()) return; // Enter must not act while the page is not live
    if (this.mode === "idle") this.dial(this.input.value.trim());
    else if (this.mode === "con") this.reply(this.input.value.trim());
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
    const { status, data } = await this.ctx.api("POST", "api/ussd", body);
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
      if (data.end) this.ctx.ended?.(this.p, this.screen);
    } else {
      this.note.textContent = data.error || "Something went wrong.";
      this.note.hidden = false;
    }
    this.render();
  }

  // type shows keys going into the reply box, as a person would press them.
  // It stops once another phone is selected or a reset clears this one: the
  // box then belongs to a phone the keys were not meant for.
  async type(keys, signal) {
    const gen = this.gen;
    this.input.value = "";
    await sleep(READ_MS, signal);
    for (const k of keys) {
      if (gen !== this.gen) return;
      this.input.value += k;
      await sleep(KEY_MS, signal);
    }
  }

  refresh() { /* a basic phone shows only what it was sent */ }

  render() {
    const p = this.p;
    const codes = this.ctx.codes();
    this.name.textContent = p ? p.label : "…";
    this.number.textContent = p ? [p.ward, p.number].filter(Boolean).join(" · ") : "";
    this.root.setAttribute("aria-label", p ? p.label + "'s phone" : "Phone");
    this.quick.textContent = "Dial " + this.code();
    const idle = this.mode === "idle";
    if (idle) {
      this.meta.textContent = "Ready";
      this.text.textContent = this.kind === "caller"
        ? "Dial " + codes.caller + "\nto report an emergency."
        : "Dial " + codes.responder + "\nfor the responder menu.";
      this.label.textContent = "Number to dial";
    } else {
      this.meta.textContent = this.mode === "con" ? "USSD" : "USSD, session ended";
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
    const off = this.busy || !p || !this.ctx.live() || Boolean(lock);
    for (const b of [this.callBtn, this.endBtn, this.quick, ...this.keypad.children]) b.disabled = off;
    this.input.disabled = off;
    this.quick.hidden = !idle;
  }

  renderInbox() { this.inbox.render(this.p); }
}
