package config

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

const approvedResponder = `status: approved
languages: [en, lg]
decline_reasons:
  transporter:
    - {code: carrying_passenger, label: {en: "Carrying a passenger", lg: "T1"}}
    - {code: too_far, label: {en: "Too far", lg: "T2"}}
    - {code: bike_or_fuel, label: {en: "Bike or fuel problem", lg: "T3"}}
    - {code: unwell, label: {en: "Unwell", lg: "T4"}}
    - {code: unsafe, label: {en: "Unsafe place or time", lg: "T5"}}
    - {code: other, label: {en: "Other", lg: "T6"}}
  vht:
    - {code: with_another_patient, label: {en: "With another patient", lg: "V1"}}
    - {code: too_far, label: {en: "Too far", lg: "V2"}}
    - {code: kit_not_ready, label: {en: "Kit not ready", lg: "V3"}}
    - {code: unwell, label: {en: "Unwell", lg: "V4"}}
    - {code: unsafe, label: {en: "Unsafe place or time", lg: "V5"}}
    - {code: other, label: {en: "Other", lg: "V6"}}
menu:
  not_registered: {en: "Not a Sauti responder number.", lg: "N"}
  alert: {en: "Sauti {ref}: {colour} {category}", lg: "Sauti {ref}: {colour} {category}"}
  accept: {en: "Accept", lg: "A"}
  decline: {en: "Decline", lg: "D"}
  availability: {en: "Availability", lg: "B"}
  no_alert: {en: "No active alert.", lg: "No"}
  accepted: {en: "Accepted {ref}. Go to {landmark}, {ward}.", lg: "{ref} {landmark} {ward}"}
  assigned: {en: "On {ref}. Go to {landmark}, {ward}.", lg: "{ref} {landmark} {ward}"}
  already_taken: {en: "Already taken, thank you.", lg: "AT"}
  already_assigned: {en: "You are on another incident.", lg: "AA"}
  no_longer_needed: {en: "No longer needed. Thank you.", lg: "NL"}
  declined: {en: "Declined. Thank you.", lg: "DD"}
  decline_why: {en: "Why?", lg: "W"}
  switch_on: {en: "On for 8 hours", lg: "O"}
  switch_off: {en: "Off", lg: "F"}
  available_until: {en: "Available until {until}.", lg: "{until}"}
  switched_off: {en: "You are off.", lg: "OFF"}
  invalid: {en: "Choose a number from the list.", lg: "I"}
`

func responderFS(doc string) fstest.MapFS {
	fs := minimalFS()
	fs["gated/responder.yaml"] = &fstest.MapFile{Data: []byte(doc)}
	return fs
}

// Decision 6 (Build Prompt §10.5): a closed list per role whose last option
// is Other, no free text; the menu copy is complete in every language.
func TestResponderMenuAndDeclineReasons(t *testing.T) {
	b, err := Load(responderFS(approvedResponder))
	if err != nil {
		t.Fatal(err)
	}
	if r := b.Responder.DeclineReasons["transporter"]; len(r) != 6 || r[2].Code != "bike_or_fuel" || r[5].Code != "other" {
		t.Fatalf("transporter reasons %+v", r)
	}
	for name, doc := range map[string]string{
		"last is not Other":      strings.Replace(approvedResponder, "    - {code: other, label: {en: \"Other\", lg: \"T6\"}}\n", "", 1),
		"too many reasons":       strings.Replace(approvedResponder, "  vht:\n", "  vht:\n    - {code: a7, label: {en: \"A\", lg: \"A\"}}\n    - {code: a8, label: {en: \"B\", lg: \"B\"}}\n    - {code: a9, label: {en: \"C\", lg: \"C\"}}\n", 1),
		"unknown role":           strings.Replace(approvedResponder, "  vht:\n", "  doctor:\n", 1),
		"reason not a code":      strings.Replace(approvedResponder, "code: too_far, label: {en: \"Too far\", lg: \"T2\"}", "code: \"Too far\", label: {en: \"Too far\", lg: \"T2\"}", 1),
		"missing menu text":      strings.Replace(approvedResponder, "  invalid: {en: \"Choose a number from the list.\", lg: \"I\"}\n", "", 1),
		"accepted without {ref}": strings.Replace(approvedResponder, "lg: \"{ref} {landmark} {ward}\"", "lg: \"x\"", 1),
	} {
		if _, err := Load(responderFS(doc)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	pending, err := Load(responderFS(strings.Replace(approvedResponder, "status: approved", "status: placeholder", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.ProductionReady(); !errors.Is(err, ErrNotProductionReady) {
		t.Fatalf("placeholder responder copy in production: %v", err)
	}
}
