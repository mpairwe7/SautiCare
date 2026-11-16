// The map's geometry: the gazetteer projected into a frame the size of the
// panel, each ward the territory nearest its centre, incidents at their
// landmark (or ward centre) and fanned out when they share one, each ward's
// responders grouped by its centre, and one line per alert. Pure: the view
// draws what layout returns.
import { ended } from "./format.js";

export const PIN_R = 12; // an incident pin's radius
export const SQUARE = 24; // a responder's square
const GAP = 4;
const FAN_MIN = 30; // a fan's least radius, so its leader lines show
const ROW = 3; // responders per line in a ward's group
const SIZE = { pin: 13, ward: 15, landmark: 11 }; // font sizes, px
const CHAR = 0.62; // a character's width per px of font size: an estimate, for placing text

// projector maps WGS84 into a width × height frame, north up, fitting the
// points inside pad and centring them. Longitude is scaled by cos(latitude)
// so a metre east is as long as a metre north.
export function projector(points, width, height, pad) {
  const lats = points.map((p) => p.lat);
  const lons = points.map((p) => p.lon);
  const [minLat, maxLat, minLon, maxLon] = [Math.min(...lats), Math.max(...lats), Math.min(...lons), Math.max(...lons)];
  const k = Math.cos(((minLat + maxLat) / 2) * (Math.PI / 180));
  const spanX = Math.max((maxLon - minLon) * k, 1e-9);
  const spanY = Math.max(maxLat - minLat, 1e-9);
  const scale = Math.min((width - 2 * pad) / spanX, (height - 2 * pad) / spanY);
  const ox = (width - spanX * scale) / 2;
  const oy = (height - spanY * scale) / 2;
  return { x: (lon) => ox + (lon - minLon) * k * scale, y: (lat) => oy + (maxLat - lat) * scale };
}

// cells returns each site's Voronoi cell in the frame [x0, y0, x1, y1]: the
// frame cut down to the points at least as near the site as any other site.
export function cells(sites, [x0, y0, x1, y1]) {
  const frame = [[x0, y0], [x1, y0], [x1, y1], [x0, y1]];
  return sites.map((s) => sites.reduce((poly, o) => (o === s ? poly : nearer(poly, s, o)), frame));
}

// nearer clips the convex polygon poly to the half-plane nearer s than o.
function nearer(poly, s, o) {
  const side = ([x, y]) => (x - (s.x + o.x) / 2) * (o.x - s.x) + (y - (s.y + o.y) / 2) * (o.y - s.y);
  const out = [];
  poly.forEach((p, i) => {
    const q = poly[(i + 1) % poly.length];
    const a = side(p);
    const b = side(q);
    if (a <= 0) out.push(p);
    if (a * b < 0) out.push([p[0] + (a / (a - b)) * (q[0] - p[0]), p[1] + (a / (a - b)) * (q[1] - p[1])]);
  });
  return out;
}

// inside: (x, y) lies in a convex polygon wound as cells winds them.
const inside = (poly, x, y) => poly.every((p, i) => {
  const q = poly[(i + 1) % poly.length];
  return (q[0] - p[0]) * (y - p[1]) - (q[1] - p[1]) * (x - p[0]) >= -1e-9;
});

const box = (x, y, w, h) => [x, y, x + w, y + h];
const overlaps = (a, b) => a[0] < b[2] && b[0] < a[2] && a[1] < b[3] && b[1] < a[3];

// beside places text of size px next to a disc of radius r at (x, y), on
// the side the unit vector (ux, uy) points to: its anchor point, its
// text-anchor, and the box it is estimated to fill.
function beside(x, y, r, ux, uy, text, size) {
  const w = text.length * size * CHAR;
  const ax = x + ux * (r + 3);
  const ay = y + uy * (r + 3);
  const anchor = ux > 0.38 ? "start" : ux < -0.38 ? "end" : "middle";
  const left = anchor === "start" ? ax : anchor === "end" ? ax - w : ax - w / 2;
  const top = uy > 0.38 ? ay : uy < -0.38 ? ay - size : ay - size / 2;
  return { x: ax, y: top + size * 0.8, anchor, box: box(left, top, w, size) };
}

// fan places n pins at (x, y): one on the point, its label up and to the
// right; more on a ring around it, a pin's width and a gap apart, the first
// at the top and on clockwise, each label outward.
function fan(x, y, n) {
  if (n === 1) return [{ x, y, ux: Math.SQRT1_2, uy: -Math.SQRT1_2 }];
  const r = Math.max(FAN_MIN, (2 * PIN_R + GAP) / (2 * Math.sin(Math.PI / n)));
  return Array.from({ length: n }, (_, i) => {
    const a = (2 * Math.PI * i) / n - Math.PI / 2;
    return { x: x + r * Math.cos(a), y: y + r * Math.sin(a), ux: Math.cos(a), uy: Math.sin(a) };
  });
}

// fanned fans list's pins and their labels around (x, y), moved as little
// as keeps them all inside the width × height frame.
function fanned(x, y, list, width, height) {
  const at = (ox, oy) => fan(ox, oy, list.length).map((pos, i) =>
    ({ ...pos, label: beside(pos.x, pos.y, PIN_R, pos.ux, pos.uy, list[i].reference.slice(-4), SIZE.pin) }));
  const boxes = at(0, 0).flatMap((p) => [box(p.x - PIN_R, p.y - PIN_R, 2 * PIN_R, 2 * PIN_R), p.label.box]);
  const edge = (k, f) => f(...boxes.map((b) => b[k]));
  return at(Math.min(Math.max(x, -edge(0, Math.min)), width - edge(2, Math.max)),
    Math.min(Math.max(y, -edge(1, Math.min)), height - edge(3, Math.max)));
}

// spots lists the points of a grid through (x, y), step px apart, that
// cover the polygon's bounding box, nearest (x, y) first.
function spots(poly, x, y, step) {
  const xs = poly.map((p) => p[0]);
  const ys = poly.map((p) => p[1]);
  const out = [];
  for (let px = x - Math.floor((x - Math.min(...xs)) / step) * step; px <= Math.max(...xs); px += step) {
    for (let py = y - Math.floor((y - Math.min(...ys)) / step) * step; py <= Math.max(...ys); py += step) out.push([px, py]);
  }
  return out.sort((a, b) => Math.hypot(a[0] - x, a[1] - y) - Math.hypot(b[0] - x, b[1] - y));
}

// short labels a responder's square: VHT 3 → V3, Transporter 2 → T2.
export const short = (label) => label.replace(/^(\w)\w*\s*/, "$1");

const t1 = (inc) => Date.parse(inc.t?.T1) || 0;

export function layout(map, phones, board, { width = 640, height = 640, pad = 56 } = {}) {
  const P = projector([...map.wards, ...map.landmarks], width, height, pad);
  const at = (p) => ({ x: P.x(p.lon), y: P.y(p.lat) });
  const centres = map.wards.map(at);
  const territory = cells(centres, [0, 0, width, height]);
  const landmarks = map.landmarks.map((l) => ({ ...l, ...at(l) }));
  const wardOf = Object.fromEntries(map.wards.map((w, i) => [w.id, { ...w, ...centres[i] }]));
  const landmarkOf = Object.fromEntries(landmarks.map((l) => [l.id, l]));

  const groups = new Map(); // place → the incidents there
  let unplaced = 0;
  for (const inc of board.incidents) {
    const place = landmarkOf[inc.landmarkId] || wardOf[inc.wardId];
    if (!place) { unplaced++; continue; }
    if (!groups.has(place.id)) groups.set(place.id, { place, list: [] });
    groups.get(place.id).list.push(inc);
  }
  const incidents = [];
  const pins = [];
  const marks = []; // the pins and their labels
  for (const { place, list } of groups.values()) {
    list.sort((a, b) => t1(a) - t1(b) || a.id.localeCompare(b.id));
    fanned(place.x, place.y, list, width, height).forEach(({ label, ...pos }, i) => {
      const inc = list[i];
      incidents.push({ id: inc.id, ref: inc.reference, severity: inc.severity || "none", state: inc.state, active: !ended(inc),
        merged: inc.state === "merged", place: place.name, x: pos.x, y: pos.y, at: list.length > 1 ? { x: place.x, y: place.y } : null, label });
      const pin = box(pos.x - PIN_R, pos.y - PIN_R, 2 * PIN_R, 2 * PIN_R);
      pins.push(pin);
      marks.push(pin, label.box);
    });
  }
  const taken = [...landmarks.map((l) => box(l.x - 4, l.y - 4, 8, 8)), ...marks]; // what is placed, for placing what comes after

  // Each ward's group, its name over its responders' squares, sits at the
  // spot nearest its centre that keeps it inside the ward and clear of the
  // pins, their labels and the landmarks.
  const responders = [];
  const wards = map.wards.map((w, i) => {
    const mine = (phones || []).filter((p) => p.kind === "responder" && p.wardId === w.id);
    const gw = Math.max(w.name.length * SIZE.ward * CHAR, Math.min(mine.length, ROW) * (SQUARE + GAP) - GAP);
    const gh = SIZE.ward + Math.ceil(mine.length / ROW) * (SQUARE + GAP);
    const c = centres[i];
    const fits = (against) => ([x, y]) => {
      const b = box(x - gw / 2 - 5, y - gh / 2 - 5, gw + 10, gh + 10);
      return [[b[0], b[1]], [b[2], b[1]], [b[2], b[3]], [b[0], b[3]]].every(([px, py]) => inside(territory[i], px, py))
        && !against.some((t) => overlaps(b, t));
    };
    const near = spots(territory[i], c.x, c.y, 8);
    // Crowded, a group covers a landmark before a pin's label, and a label before a pin.
    const [gx, gy] = near.find(fits(taken)) || near.find(fits(marks)) || near.find(fits(pins)) || near.find(fits([])) || [c.x, c.y];
    const top = gy - gh / 2;
    taken.push(box(gx - gw / 2, top, gw, gh));
    mine.forEach((p, j) => {
      const row = Math.floor(j / ROW);
      const cols = Math.min(ROW, mine.length - row * ROW);
      responders.push({ id: p.id, label: p.label, short: short(p.label), role: p.role, ward: w.name,
        x: gx - (cols * (SQUARE + GAP) - GAP) / 2 + SQUARE / 2 + (j % ROW) * (SQUARE + GAP),
        y: top + SIZE.ward + GAP + row * (SQUARE + GAP) + SQUARE / 2 });
    });
    return { id: w.id, name: w.name, cx: c.x, cy: c.y, cell: territory[i], label: { x: gx, y: top + SIZE.ward * 0.8 } };
  });

  // A landmark's name shows where there is room for it: away from its ward's
  // centre, else towards it, else not at all.
  const named = landmarks.map((l) => {
    const side = l.x >= wardOf[l.ward]?.x ? 1 : -1;
    const label = [side, -side].map((s) => beside(l.x, l.y, 3, s, 0, l.name, SIZE.landmark))
      .find((t) => t.box[0] >= 0 && t.box[1] >= 0 && t.box[2] <= width && t.box[3] <= height && !taken.some((b) => overlaps(t.box, b))) || null;
    if (label) taken.push(label.box);
    return { ...l, label };
  });

  const responderOf = Object.fromEntries(responders.map((r) => [r.label, r]));
  const placedOf = Object.fromEntries(incidents.map((i) => [i.id, i]));
  const links = [];
  for (const inc of board.incidents) {
    for (const a of inc.alerts) {
      const from = responderOf[a.responder];
      const to = placedOf[inc.id];
      if (from && to) links.push({ key: inc.id + ":" + a.id, incident: inc.id, responder: a.responder, outcome: a.outcome, from, to });
    }
  }
  return { width, height, wards, landmarks: named, incidents, responders, links, unplaced };
}

const count = (n, one, many) => `${n} ${n === 1 ? one : many}`;

// summary is the map in words, for readers who cannot see it.
export function summary(lay) {
  const bySev = {};
  for (const i of lay.incidents) if (i.active) bySev[i.severity] = (bySev[i.severity] || 0) + 1;
  const sev = Object.entries(bySev).map(([s, n]) => `${n} ${s === "none" ? "not yet triaged" : s}`).join(", ");
  const active = lay.incidents.filter((i) => i.active).length;
  const byOutcome = {};
  for (const l of lay.links) byOutcome[l.outcome] = (byOutcome[l.outcome] || 0) + 1;
  const lines = Object.entries(byOutcome).map(([o, n]) => `${n} ${o.replaceAll("_", " ")}`).join(", ");
  const perWard = lay.wards.map((w) => `${lay.responders.filter((r) => r.ward === w.name).length} in ${w.name}`).join(", ");
  return [
    `${count(lay.wards.length, "ward", "wards")} and ${count(lay.landmarks.length, "landmark", "landmarks")}.`,
    active ? `${count(active, "open incident", "open incidents")} on the map: ${sev}.` : "No open incident on the map.",
    lay.unplaced ? `${count(lay.unplaced, "incident has", "incidents have")} no place yet.` : "",
    `Responders: ${perWard}.`,
    lay.links.length ? `${count(lay.links.length, "alert line", "alert lines")}: ${lines}.` : "",
  ].filter(Boolean).join(" ");
}
