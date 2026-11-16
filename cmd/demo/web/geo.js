// The map's geometry: the gazetteer projected onto an SVG, incidents at
// their landmark (or ward centre), responders in their ward, and one line
// per alert. Pure: the view draws what layout returns.
import { ended } from "./format.js";

// projector maps WGS84 to SVG units, north up. Longitude is scaled by
// cos(latitude) so a metre east is as long as a metre north.
export function projector(points, width, pad) {
  const lats = points.map((p) => p.lat);
  const lons = points.map((p) => p.lon);
  const [minLat, maxLat, minLon, maxLon] = [Math.min(...lats), Math.max(...lats), Math.min(...lons), Math.max(...lons)];
  const k = Math.cos(((minLat + maxLat) / 2) * (Math.PI / 180));
  const scale = (width - 2 * pad) / Math.max((maxLon - minLon) * k, 1e-9);
  return {
    width,
    height: Math.round((maxLat - minLat) * scale + 2 * pad),
    x: (lon) => pad + (lon - minLon) * k * scale,
    y: (lat) => pad + (maxLat - lat) * scale,
  };
}

// around places n marks on a circle of radius r centred on (x, y), the
// first at the top; one mark sits on the centre.
function around(x, y, n, r) {
  if (n === 1) return [{ x, y }];
  return Array.from({ length: n }, (_, i) => {
    const a = (2 * Math.PI * i) / n - Math.PI / 2;
    return { x: x + r * Math.cos(a), y: y + r * Math.sin(a) };
  });
}

// short labels a responder's square: VHT 3 → V3, Transporter 2 → T2.
export const short = (label) => label.replace(/^(\w)\w*\s*/, "$1");

const WARD_PAD = 14;
const LABEL_BAND = 16; // above a ward's landmarks: its name
const DOCK = 26; // below them: its responders

export function layout(map, phones, board, { width = 360, pad = 40 } = {}) {
  const P = projector([...map.wards, ...map.landmarks], width, pad);
  const landmarks = map.landmarks.map((l) => ({ ...l, x: P.x(l.lon), y: P.y(l.lat) }));
  const wards = map.wards.map((w) => {
    const pts = [{ x: P.x(w.lon), y: P.y(w.lat) }, ...landmarks.filter((l) => l.ward === w.id)];
    const xs = pts.map((p) => p.x);
    const ys = pts.map((p) => p.y);
    const x = Math.min(...xs) - WARD_PAD;
    const y = Math.min(...ys) - WARD_PAD - LABEL_BAND;
    return { id: w.id, name: w.name, cx: P.x(w.lon), cy: P.y(w.lat), x, y,
      w: Math.max(...xs) + WARD_PAD - x, h: Math.max(...ys) + WARD_PAD + DOCK - y };
  });
  const wardOf = Object.fromEntries(wards.map((w) => [w.id, w]));
  const landmarkOf = Object.fromEntries(landmarks.map((l) => [l.id, l]));

  const groups = new Map(); // anchor → incidents placed there
  const unplaced = [];
  for (const inc of board.incidents) {
    const at = landmarkOf[inc.landmarkId] || wardOf[inc.wardId];
    if (!at) { unplaced.push(inc); continue; }
    const key = at.id;
    if (!groups.has(key)) groups.set(key, { at, list: [] });
    groups.get(key).list.push(inc);
  }
  const incidents = [];
  for (const { at, list } of groups.values()) {
    const ax = at.cx ?? at.x;
    const ay = at.cy ?? at.y;
    around(ax, ay, list.length, Math.max(11, 3.6 * list.length)).forEach((pos, i) => {
      const inc = list[i];
      incidents.push({ id: inc.id, ref: inc.reference, severity: inc.severity || "none", state: inc.state, active: !ended(inc),
        merged: inc.state === "merged", place: at.name, ...pos });
    });
  }

  const responders = [];
  for (const w of wards) {
    const mine = (phones || []).filter((p) => p.kind === "responder" && p.wardId === w.id);
    const gap = Math.min(18, (w.w - 12) / Math.max(mine.length, 1));
    const x0 = w.x + w.w / 2 - (gap * (mine.length - 1)) / 2;
    mine.forEach((p, i) => responders.push({ id: p.id, label: p.label, short: short(p.label), role: p.role, ward: w.name,
      x: x0 + gap * i, y: w.y + w.h - DOCK / 2 }));
  }
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
  return { width: P.width, height: P.height, wards, landmarks, incidents, responders, links, unplaced: unplaced.length };
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
