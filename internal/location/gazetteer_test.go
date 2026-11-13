package location

import (
	"os"
	"slices"
	"strings"
	"testing"
)

const sample = `
version: "test-1"
status: placeholder
wards:
  - id: w01
    name: {en: "Test Ward 1", lg: "Test Ward 1"}
    centre: {lat: 0.3800, lon: 32.5600}
  - id: w02
    name: {en: "Test Ward 2", lg: "Test Ward 2"}
    centre: {lat: 0.3950, lon: 32.5650}
    zones: [{col: 903, row: 87}]
landmarks:
  - id: w01_l1
    ward: w01
    name: {en: "Market", lg: "Akatale"}
    point: {lat: 0.3812, lon: 32.5588}
    menu: 1
    aliases: ["Kawempe Market"]
  - id: w01_l2
    ward: w01
    name: {en: "Clinic", lg: "Ddwaliro"}
    point: {lat: 0.3790, lon: 32.5612}
    menu: 2
  - id: w01_x
    ward: w01
    name: {en: "Old Taxi Stage", lg: "Siteegi"}
    point: {lat: 0.3700, lon: 32.5500}
`

func TestParseFileDerivesCells(t *testing.T) {
	f, err := ParseFile([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != "test-1" || f.Status != "placeholder" || len(f.Wards) != 2 || len(f.Landmarks) != 3 {
		t.Fatalf("file = %+v", f)
	}
	w1 := f.Wards[0]
	if w1.Centre != (Cell{Col: 902, Row: 84}) {
		t.Fatalf("w01 centre = %+v", w1.Centre)
	}
	// A ward's zones are its centre, its listed zones and its landmarks' cells.
	l1, _ := CellAt(0.3812, 32.5588)
	lx, _ := CellAt(0.3700, 32.5500)
	for _, c := range []Cell{w1.Centre, l1, lx} {
		if !slices.Contains(w1.Zones, c) {
			t.Errorf("w01 zones %v lack %v", w1.Zones, c)
		}
	}
	if !slices.Contains(f.Wards[1].Zones, (Cell{Col: 903, Row: 87})) {
		t.Errorf("w02 zones %v lack the listed zone", f.Wards[1].Zones)
	}
	menu := f.Menu("w01")
	if len(menu) != 2 || menu[0].ID != "w01_l1" || menu[1].ID != "w01_l2" {
		t.Fatalf("menu = %+v", menu)
	}
	// Names in both languages and listed aliases are all matchable.
	al := f.Landmarks[0].Aliases
	for _, want := range []string{"market", "akatale", "kawempe market"} {
		if !slices.Contains(al, want) {
			t.Errorf("aliases %v lack %q", al, want)
		}
	}
}

func TestParseFileRejectsInvalidGazetteers(t *testing.T) {
	sixMenu := "version: v\nstatus: placeholder\nwards:\n  - {id: w01, name: {en: A, lg: A}, centre: {lat: 0.38, lon: 32.56}}\nlandmarks:\n"
	for i := 1; i <= 6; i++ {
		sixMenu += "  - {id: l" + string(rune('0'+i)) + ", ward: w01, name: {en: L, lg: L}, point: {lat: 0.38, lon: 32.56}, menu: " + string(rune('0'+i)) + "}\n"
	}
	// Wards 1-8 and "Somewhere else" fill the single-digit ward menu.
	nineWards := "version: v\nstatus: placeholder\nwards:\n"
	for i := 1; i <= 9; i++ {
		nineWards += "  - {id: w0" + string(rune('0'+i)) + ", name: {en: W, lg: W}, centre: {lat: 0.38, lon: 32.56}}\n"
	}
	nineWards += "landmarks: []\n"
	cases := map[string]string{
		"nine wards":          nineWards,
		"six menu landmarks":  sixMenu,
		"unknown key":         strings.Replace(sample, "status: placeholder", "status: placeholder\ncolour: red", 1),
		"no version":          strings.Replace(sample, `version: "test-1"`, `version: ""`, 1),
		"bad status":          strings.Replace(sample, "status: placeholder", "status: draft", 1),
		"duplicate landmark":  strings.Replace(sample, "id: w01_l2", "id: w01_l1", 1),
		"unknown ward":        strings.Replace(sample, "ward: w01\n    name: {en: \"Clinic\"", "ward: w09\n    name: {en: \"Clinic\"", 1),
		"menu position reuse": strings.Replace(sample, "menu: 2", "menu: 1", 1),
		"menu position 6":     strings.Replace(sample, "menu: 2", "menu: 6", 1),
		"id not a code":       strings.Replace(sample, "id: w01_l2", "id: \"0772123456\"", 1),
		"swapped coordinates": strings.Replace(sample, "{lat: 0.3790, lon: 32.5612}", "{lat: 32.5612, lon: 0.3790}", 1),
		"missing lg name":     strings.Replace(sample, `{en: "Clinic", lg: "Ddwaliro"}`, `{en: "Clinic"}`, 1),
		"no wards":            "version: v\nstatus: placeholder\nwards: []\nlandmarks: []\n",
	}
	for name, doc := range cases {
		if _, err := ParseFile([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNormalise(t *testing.T) {
	for in, want := range map[string]string{
		"Kawempe  Market!":  "kawempe market",
		" Mulago-Hospital ": "mulago hospital",
		"Ssebaggala's shop": "ssebaggala s shop",
		"ÉGLISE  St. Peter": "eglise st peter",
		"":                  "",
		"***":               "",
		"Kawempe*Mkt":       "kawempe mkt",
		"\tTAXI\nstage 2 ":  "taxi stage 2",
	} {
		if got := Normalise(in); got != want {
			t.Errorf("Normalise(%q) = %q, want %q", in, got, want)
		}
	}
}

// The committed gazetteer parses, and a placeholder is never production-ready.
func TestRepositoryGazetteer(t *testing.T) {
	data, err := os.ReadFile("../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseFile(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range f.Wards {
		if n := len(f.Menu(w.ID)); n == 0 {
			t.Errorf("ward %s has no menu landmarks", w.ID)
		}
	}
	if f.Status == StatusPlaceholder && ProductionReady(f) == nil {
		t.Fatal("placeholder gazetteer reported production-ready")
	}
	approved := f
	approved.Status = StatusApproved
	if err := ProductionReady(approved); err != nil {
		t.Fatalf("approved gazetteer refused: %v", err)
	}
}

const withDispatch = sample + `stages:
  - {id: w01_stage, ward: w01, name: {en: "Test Stage 1", lg: "Test Stage 1"}, point: {lat: 0.3805, lon: 32.5595}}
facilities:
  - {id: hc_test_1, name: {en: "Test Health Centre", lg: "Test Health Centre"}, point: {lat: 0.3790, lon: 32.5610}, forwarding_enabled: true, phone: "+256000000001"}
  - {id: hc_test_2, name: {en: "Test Clinic", lg: "Test Clinic"}, point: {lat: 0.3700, lon: 32.5700}}
`

// Stages (where transporters wait) and facilities (forwarding targets) are
// gazetteer reference data with cells (Build Prompt §9 Reference, §12).
func TestParseFileReadsStagesAndFacilities(t *testing.T) {
	f, err := ParseFile([]byte(withDispatch))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stages) != 1 || f.Stages[0].WardID != "w01" || len(f.Facilities) != 2 {
		t.Fatalf("stages %+v facilities %+v", f.Stages, f.Facilities)
	}
	want, _ := CellAt(0.3805, 32.5595)
	if f.Stages[0].Cell != want || !f.Facilities[0].ForwardingEnabled || f.Facilities[1].ForwardingEnabled || f.Facilities[0].Phone != "+256000000001" {
		t.Fatalf("stage %+v facility %+v", f.Stages[0], f.Facilities[0])
	}
	if s, ok := f.Stage("w01_stage"); !ok || s.ID != "w01_stage" {
		t.Fatal("Stage lookup")
	}
	for name, doc := range map[string]string{
		"stage in unknown ward":    strings.Replace(withDispatch, "ward: w01, name: {en: \"Test Stage 1\"", "ward: w09, name: {en: \"Test Stage 1\"", 1),
		"id shared with landmark":  strings.Replace(withDispatch, "id: w01_stage", "id: w01_l1", 1),
		"bad facility phone":       strings.Replace(withDispatch, `phone: "+256000000001"`, `phone: "call me"`, 1),
		"forwarding without phone": strings.Replace(withDispatch, `forwarding_enabled: true, phone: "+256000000001"`, `forwarding_enabled: true`, 1),
	} {
		if _, err := ParseFile([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
