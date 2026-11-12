package config

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// approvedTool1 is a synthetic, fully translated catalogue for tests (the
// real wording is protocol content and is not invented here).
const approvedTool1 = `status: approved
languages: [en, lg]
language:
  prompt: "Sauti\n"
  options:
    - {value: en, label: {en: English, lg: English}}
    - {value: lg, label: {en: Luganda, lg: Luganda}}
danger:
  prompt: {en: "Q2 en", lg: "Q2 lg"}
  options:
    - {value: "yes", label: {en: "Yes", lg: "Yee"}}
    - {value: "no", label: {en: "No", lg: "Nedda"}}
    - {value: "unknown", label: {en: "Not sure", lg: "Simanyi"}}
category:
  prompt: {en: "Q3 en", lg: "Q3 lg"}
  options:
    - {value: trauma, label: {en: "Injury", lg: "Obuvune"}}
    - {value: acute_medical, label: {en: "Illness", lg: "Obulwadde"}}
age_band:
  prompt: {en: "Q4 en", lg: "Q4 lg"}
  options:
    - {value: under_5, label: {en: "Under 5", lg: "Wansi wa 5"}, child: true}
    - {value: age_18_59, label: {en: "18-59", lg: "18-59"}}
specific_danger:
  prompts:
    trauma: {en: "Q5 trauma en", lg: "Q5 trauma lg"}
    acute_medical: {en: "Q5 med en", lg: "Q5 med lg"}
    acute_medical_child: {en: "Q5 child en", lg: "Q5 child lg"}
  options:
    - {value: "yes", label: {en: "Yes", lg: "Yee"}}
    - {value: "no", label: {en: "No", lg: "Nedda"}}
ward:
  prompt: {en: "Q6 en", lg: "Q6 lg"}
  elsewhere_label: {en: "Somewhere else", lg: "Awalala"}
landmark:
  prompt: {en: "Q7 en", lg: "Q7 lg"}
  other_label: {en: "Other", lg: "Ekirala"}
  other_prompt: {en: "Type the place", lg: "Wandiika ekifo"}
callback:
  prompt: {en: "Q8 en", lg: "Q8 lg"}
  prompt_withheld: {en: "Q8 number en", lg: "Q8 number lg"}
  same_number_label: {en: "This number", lg: "Ennamba eno"}
messages:
  out_of_area: {en: "Call 999 or 112", lg: "Kuba 999 oba 112"}
  help_coming: {en: "HELP IS COMING {ref} {first_aid}", lg: "OBUYAMBI BUJJA {ref} {first_aid}"}
  invalid: {en: "Invalid choice", lg: "Si kituufu"}
  first_aid:
    trauma: {en: "Press on bleeding", lg: "Nyiga"}
    acute_medical: {en: "Stay with them", lg: "Beera nabo"}
`

func tool1FS(doc string) fstest.MapFS {
	fs := minimalFS()
	fs["gated/tool1.yaml"] = &fstest.MapFile{Data: []byte(doc)}
	return fs
}

func TestTool1CatalogueLoads(t *testing.T) {
	b, err := Load(tool1FS(approvedTool1))
	if err != nil {
		t.Fatal(err)
	}
	c := b.Tool1
	if len(c.Category.Options) != 2 || c.SpecificDanger.Prompts["acute_medical_child"]["lg"] != "Q5 child lg" ||
		!c.AgeBand.Options[0].Child || c.Messages.HelpComing["en"] != "HELP IS COMING {ref} {first_aid}" {
		t.Fatalf("catalogue = %+v", c)
	}
	if err := b.ProductionReady(); err != nil {
		t.Fatalf("approved catalogue not ready: %v", err)
	}
}

func TestTool1CatalogueIsComplete(t *testing.T) {
	cases := map[string]string{
		"missing lg prompt":          strings.Replace(approvedTool1, `prompt: {en: "Q2 en", lg: "Q2 lg"}`, `prompt: {en: "Q2 en"}`, 1),
		"missing lg label":           strings.Replace(approvedTool1, `label: {en: "Injury", lg: "Obuvune"}`, `label: {en: "Injury"}`, 1),
		"category without Q5":        strings.Replace(approvedTool1, "    trauma: {en: \"Q5 trauma en\", lg: \"Q5 trauma lg\"}\n", "", 1),
		"child band without child Q": strings.Replace(approvedTool1, "    acute_medical_child: {en: \"Q5 child en\", lg: \"Q5 child lg\"}\n", "", 1),
		"value not a code":           strings.Replace(approvedTool1, "value: trauma,", "value: \"Trauma\",", 1),
		"duplicate value":            strings.Replace(approvedTool1, "value: acute_medical,", "value: trauma,", 1),
		"no {ref} on help screen":    strings.Replace(approvedTool1, `lg: "OBUYAMBI BUJJA {ref} {first_aid}"`, `lg: "OBUYAMBI BUJJA"`, 1),
		"out of area without 112":    strings.Replace(approvedTool1, `lg: "Kuba 999 oba 112"`, `lg: "Kuba 999"`, 1),
		"danger option missing":      strings.Replace(approvedTool1, "    - {value: \"unknown\", label: {en: \"Not sure\", lg: \"Simanyi\"}}\n", "", 1),
		"non-GSM character":          strings.Replace(approvedTool1, `"Q3 lg"`, `"Q3 lg ŋ"`, 1),
		"bad status":                 strings.Replace(approvedTool1, "status: approved", "status: draft", 1),
		"no first aid line":          strings.Replace(approvedTool1, "    trauma: {en: \"Press on bleeding\", lg: \"Nyiga\"}\n", "", 1),
		"too many options":           strings.Replace(approvedTool1, "  options:\n    - {value: en,", "  options:\n    - {value: a1, label: {en: a, lg: a}}\n    - {value: a2, label: {en: a, lg: a}}\n    - {value: a3, label: {en: a, lg: a}}\n    - {value: a4, label: {en: a, lg: a}}\n    - {value: a5, label: {en: a, lg: a}}\n    - {value: a6, label: {en: a, lg: a}}\n    - {value: a7, label: {en: a, lg: a}}\n    - {value: a8, label: {en: a, lg: a}}\n    - {value: en,", 1),
	}
	for name, doc := range cases {
		if _, err := Load(tool1FS(doc)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestIsGSM7(t *testing.T) {
	for s, want := range map[string]bool{
		"HELP IS COMING\nIncident 123 456 7890": true,
		"Kuba 999 oba 112. Éè@£$¥":              true,
		"ŋ":                                     false,
		"€":                                     false, // extension table: two septets on some networks
		"{ref}":                                 false,
		"emoji 🚑":                               false,
	} {
		if got := IsGSM7(s); got != want {
			t.Errorf("IsGSM7(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestProductionReadyRefusesPlaceholderTool1(t *testing.T) {
	for name, doc := range map[string]string{
		"placeholder status":  strings.Replace(approvedTool1, "status: approved", "status: placeholder", 1),
		"pending marker":      strings.Replace(approvedTool1, `"Q2 en"`, `"PENDING-TOOL1 Q2"`, 1),
		"pending translation": strings.Replace(approvedTool1, `"Q3 lg"`, `"PENDING-TRANSLATION"`, 1),
	} {
		b, err := Load(tool1FS(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := b.ProductionReady(); !errors.Is(err, ErrNotProductionReady) {
			t.Errorf("%s: ProductionReady = %v", name, err)
		}
	}
}
