// Formatting shared by the derivations and the views. Pure: no DOM.

// An incident reference as screens and SMS write it: 853 402 4461.
export const REF = /[0-9]{3} [0-9]{3} [0-9]{4}/;

export const ended = (inc) => inc.state === "closed" || inc.state === "merged";

// mmss formats a duration: 0:07, 1:42, 12:05.
export function mmss(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
}

// words turns a code into words: no_eligible_responder → no eligible responder.
export const words = (code) => String(code || "").replaceAll("_", " ").replaceAll(".", " ");

// payload parses an event's coded payload. The board masks numbers in it,
// which can leave text that is not JSON; that reads as no fields.
export function payload(e) {
  try {
    return JSON.parse(e.payload) || {};
  } catch {
    return {};
  }
}
