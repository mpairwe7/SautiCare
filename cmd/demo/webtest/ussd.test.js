import { test } from "node:test";
import assert from "node:assert/strict";
import { parseScreen, placeKind, keyFor, progressOf } from "../web/ussd.js";

const map = {
  wards: [{ id: "w01", name: "Test Ward 1" }, { id: "w02", name: "Test Ward 2" }, { id: "w03", name: "Test Ward 3" }],
  landmarks: [{ id: "w01_market", ward: "w01", name: "Test Market 1" }, { id: "w01_clinic", ward: "w01", name: "Test Clinic 1" }],
};

test("a screen splits into its prompt and its numbered choices", () => {
  const s = parseScreen("PENDING-TOOL1 Age of the person?\n1 Under 5\n2 5 to 17\n3 18 to 59\n4 60 or more");
  assert.equal(s.prompt, "PENDING-TOOL1 Age of the person?");
  assert.deepEqual(s.options.map((o) => o.key), ["1", "2", "3", "4"]);
  assert.equal(s.options[2].label, "18 to 59");
});

test("an incident number in the prompt is not taken for a choice", () => {
  const s = parseScreen("PENDING-RESPONDER Sauti 284 262 7491: RED Injury\n1 Accept\n2 Decline\n3 Availability");
  assert.equal(s.prompt, "PENDING-RESPONDER Sauti 284 262 7491: RED Injury");
  assert.deepEqual(s.options, [{ key: "1", label: "Accept" }, { key: "2", label: "Decline" }, { key: "3", label: "Availability" }]);
});

test("a closing screen has no choices", () => {
  const s = parseScreen("HELP IS COMING\nIncident 284 262 7491\nPENDING-TOOL1 first aid line");
  assert.equal(s.options.length, 0);
  assert.match(s.prompt, /HELP IS COMING/);
});

test("ward and landmark screens are told apart from other screens", () => {
  const ward = parseScreen("Which ward?\n1 Test Ward 1\n2 Test Ward 2\n3 Test Ward 3\n4 Not sure");
  assert.equal(placeKind(ward.options, map).kind, "ward");
  assert.equal(keyFor(ward.options, map, "w02"), "2");
  const lm = parseScreen("Nearest place?\n1 Test Market 1\n2 Test Clinic 1\n3 Other place");
  assert.equal(placeKind(lm.options, map).kind, "landmark");
  assert.equal(keyFor(lm.options, map, "w01_clinic"), "2");
  assert.equal(keyFor(lm.options, map, "w02_school"), "");
  assert.equal(placeKind(parseScreen("Age?\n1 Under 5\n2 5 to 17").options, map).kind, "");
});

test("progress never passes the whole bar", () => {
  assert.equal(progressOf(0), 0);
  assert.equal(progressOf(4), 0.5);
  assert.equal(progressOf(12), 1);
});
