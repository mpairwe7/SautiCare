// USSD screens as data, so a smartphone can show the same service as tappable
// choices. The smartphone views send the same keys to the same USSD service
// as the basic phone; only the presentation differs. Pure.

const OPTION = /^\s*(\d{1,2})\s+(\S.*)$/;

// parseScreen splits a screen into its prompt (the lines before the first
// choice), its choices ({key, label}, in order) and any lines after them.
export function parseScreen(text) {
  const prompt = [];
  const after = [];
  const options = [];
  for (const line of String(text || "").split("\n")) {
    const m = OPTION.exec(line);
    if (m) options.push({ key: m[1], label: m[2].trim() });
    else if (line.trim()) (options.length ? after : prompt).push(line.trim());
  }
  return { prompt: prompt.join("\n"), options, after: after.join("\n") };
}

// placeKind tells a ward or landmark screen from its choices: most of them
// name the gazetteer's wards, or one ward's landmarks. It returns the kind
// and, for each choice that names a place, the place's ID.
export function placeKind(options, map) {
  if (!map || options.length < 2) return { kind: "", ids: {} };
  const byName = (list) => Object.fromEntries((list || []).map((p) => [p.name.toLowerCase(), p.id]));
  for (const [kind, names] of [["ward", byName(map.wards)], ["landmark", byName(map.landmarks)]]) {
    const ids = {};
    for (const o of options) if (names[o.label.toLowerCase()]) ids[o.key] = names[o.label.toLowerCase()];
    if (Object.keys(ids).length >= Math.max(2, options.length - 1)) return { kind, ids };
  }
  return { kind: "", ids: {} };
}

// keyFor returns the key of the choice naming place id on a ward or landmark
// screen, or "" when the screen does not offer it.
export function keyFor(options, map, id) {
  const { ids } = placeKind(options, map);
  return Object.keys(ids).find((k) => ids[k] === id) || "";
}

// progressOf estimates how far a caller is through Tool 1 from the screens
// answered so far, for a progress bar (the screen count is not shown).
export const TOOL1_SCREENS = 8;
export const progressOf = (answered) => Math.min(1, answered / TOOL1_SCREENS);
