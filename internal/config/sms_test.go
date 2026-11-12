package config

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

const approvedSMS = `status: approved
languages: [en, lg]
templates:
  alert: {en: "Sauti {ref}: {colour} {category}. Dial *384*2# to answer.", lg: "Sauti {ref}: {colour} {category}. Kuba *384*2#."}
  alert_amended: {en: "Sauti {ref}: {colour} {category} at {landmark}, {ward}. Dial *384*2#.", lg: "Sauti {ref}: {colour} {category}, {landmark}, {ward}."}
  stand_down: {en: "Sauti {ref}: no longer needed. Thank you.", lg: "Sauti {ref}: tekyetaagisa. Webale."}
  availability_expiring: {en: "Sauti: available until {time}. Dial *384*2# to renew.", lg: "Sauti: okutuusa {time}. Kuba *384*2#."}
  helper_on_the_way: {en: "Sauti {ref}: help is on the way.", lg: "Sauti {ref}: obuyambi bujja."}
  forwarded_to_facility: {en: "Sauti {ref}: {facility_name} has your request. Call {facility_phone}.", lg: "Sauti {ref}: {facility_name}. Kuba {facility_phone}."}
  facility_forward: {en: "Sauti {ref}: {colour} {category}, {ward}, {landmark}.", lg: "Sauti {ref}: {colour} {category}, {ward}, {landmark}."}
`

func smsFS(doc string) fstest.MapFS {
	fs := minimalFS()
	fs["gated/sms.yaml"] = &fstest.MapFile{Data: []byte(doc)}
	return fs
}

func TestSMSTemplatesLoadAndRender(t *testing.T) {
	b, err := Load(smsFS(approvedSMS))
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.SMS.Render(SMSForwardedToFacility, "lg", SMSValues{Ref: "123 456 7890", FacilityName: "Test HC", FacilityPhone: "+256000000001"})
	if err != nil || got != "Sauti 123 456 7890: Test HC. Kuba +256000000001." {
		t.Fatalf("render: %q %v", got, err)
	}
	if got, _ := b.SMS.Render(SMSStandDown, "fr", SMSValues{Ref: "1"}); got != "Sauti 1: no longer needed. Thank you." {
		t.Fatalf("unknown language falls back to English: %q", got)
	}
	if _, err := b.SMS.Render("party", "en", SMSValues{}); err == nil {
		t.Fatal("unknown template rendered")
	}
}

// Hard constraint 6 and decision 1: SMS carry the incident number, colour,
// category and — only once the amendment allows it — ward and landmark.
// Never a patient name, a clinical answer or the caller's number: the
// values have no field for them, and a template naming anything else is
// refused. Every template, rendered with long values, fits one GSM-7 SMS.
func TestSMSTemplatesAreCompliant(t *testing.T) {
	for name, doc := range map[string]string{
		"location before the amendment": strings.Replace(approvedSMS, `"Sauti {ref}: {colour} {category}. Dial *384*2# to answer."`, `"Sauti {ref}: {colour} {category} at {landmark}."`, 1),
		"caller number":                 strings.Replace(approvedSMS, `"Sauti {ref}: help is on the way."`, `"Sauti {ref}: help is on the way to {caller}."`, 1),
		"patient name":                  strings.Replace(approvedSMS, `"Sauti {ref}: no longer needed. Thank you."`, `"Sauti {ref}: {patient} no longer needs you."`, 1),
		"clinical answer":               strings.Replace(approvedSMS, `{colour} {category}. Kuba`, `{colour} {category} {danger}. Kuba`, 1),
		"facility number to responder":  strings.Replace(approvedSMS, `"Sauti {ref}: no longer needed. Thank you."`, `"Sauti {ref}: call {facility_phone}."`, 1),
		"hard-coded phone number":       strings.Replace(approvedSMS, `"Sauti {ref}: help is on the way."`, `"Sauti {ref}: help is on the way. Call 0772123456."`, 1),
		"missing template":              strings.Replace(approvedSMS, "  stand_down: {en: \"Sauti {ref}: no longer needed. Thank you.\", lg: \"Sauti {ref}: tekyetaagisa. Webale.\"}\n", "", 1),
		"unknown template":              approvedSMS + "  promo: {en: \"Buy now\", lg: \"Gula\"}\n",
		"missing language":              strings.Replace(approvedSMS, `, lg: "Sauti {ref}: obuyambi bujja."`, ``, 1),
		"non-GSM character":             strings.Replace(approvedSMS, "Webale.", "Webale ŋ.", 1),
		"too long":                      strings.Replace(approvedSMS, `"Sauti {ref}: help is on the way."`, `"`+strings.Repeat("x", 150)+` {ref}"`, 1),
		"bad status":                    strings.Replace(approvedSMS, "status: approved", "status: draft", 1),
	} {
		if _, err := Load(smsFS(doc)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	// The repository's templates pass the same checks, and no rendered SMS
	// to a responder or facility carries a phone number.
	b, err := Load(repoFS(t))
	if err != nil {
		t.Fatal(err)
	}
	phone := regexp.MustCompile(`\+?[0-9]{9,}`)
	for _, name := range append(SMSResponderTemplates(), SMSFacilityForward) {
		for _, lang := range b.SMS.Languages {
			got, err := b.SMS.Render(name, lang, worstCaseSMS)
			if err != nil || phone.MatchString(strings.ReplaceAll(strings.ReplaceAll(got, worstCaseSMS.Ref, ""), " ", "")) {
				t.Errorf("%s/%s: %q %v", name, lang, got, err)
			}
		}
	}
}

func TestProductionReadyRefusesPendingSMS(t *testing.T) {
	for name, doc := range map[string]string{
		"placeholder status": strings.Replace(approvedSMS, "status: approved", "status: placeholder", 1),
		"pending marker":     strings.Replace(approvedSMS, `"Sauti {ref}: help is on the way."`, `"PENDING-SMS {ref}"`, 1),
	} {
		b, err := Load(smsFS(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := b.ProductionReady(); !errors.Is(err, ErrNotProductionReady) {
			t.Errorf("%s: ProductionReady = %v", name, err)
		}
	}
}

func repoFS(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS("../../config")
}
