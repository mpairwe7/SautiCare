// Element builders. Every value from the server is set as text or an
// attribute, never as HTML.

function build(el, attrs, kids) {
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "text") el.textContent = v;
    else if (k === "class") el.setAttribute("class", v);
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat(Infinity)) if (kid !== null && kid !== undefined && kid !== false) el.append(kid);
  return el;
}

// h builds an HTML element: h("p", {class: "x", text: "y"}, child, ...).
export const h = (tag, attrs, ...kids) => build(document.createElement(tag), attrs, kids);

// svg builds an SVG element the same way.
export const svg = (tag, attrs, ...kids) => build(document.createElementNS("http://www.w3.org/2000/svg", tag), attrs, kids);

export function clock(d) {
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
