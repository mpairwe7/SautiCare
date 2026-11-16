// The responder autopilot: it answers alerts sent after it was switched on,
// each at most once, after a delay, through the real responder menu
// (scenarios.js answerAlert). Pure: plan decides, the app acts.
import { ended } from "./format.js";

export const AUTOPILOT = { enabled: false, accept: 60, decline: 25, ignore: 15, delayMs: 8000 };

export const alertKey = (ref, responder) => ref + "|" + responder;

// choose picks accept, decline or ignore for r in [0, 1), in proportion to
// the shares; with every share at 0 it ignores.
export function choose(s, r) {
  const total = s.accept + s.decline + s.ignore;
  const x = r * total;
  if (!total || x >= s.accept + s.decline) return "ignore";
  return x < s.accept ? "accept" : "decline";
}

// plan returns a decision for every pending alert sent since `since` that
// `decided` does not hold yet: what to do, and when (the alert's send time
// plus the delay, jittered by ±40%). The caller adds each key to decided,
// so a later poll of the same board plans nothing new.
export function plan(board, phones, s, decided, since, rand = Math.random) {
  const phoneOf = Object.fromEntries((phones || []).filter((p) => p.kind === "responder").map((p) => [p.label, p.id]));
  const out = [];
  for (const inc of board.incidents) {
    if (ended(inc)) continue;
    for (const a of inc.alerts) {
      const key = alertKey(inc.reference, a.responder);
      const sent = Date.parse(a.sentAt);
      if (a.outcome !== "pending" || sent < since || decided.has(key) || !phoneOf[a.responder]) continue;
      out.push({ key, ref: inc.reference, responder: a.responder, phone: phoneOf[a.responder], action: choose(s, rand()),
        at: sent + s.delayMs * (0.6 + 0.8 * rand()) });
    }
  }
  return out;
}
