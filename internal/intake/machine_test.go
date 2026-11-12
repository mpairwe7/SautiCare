package intake

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
)

func repoBundle(t *testing.T) config.Bundle {
	t.Helper()
	b, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func repoGazetteer(t *testing.T) location.File {
	t.Helper()
	raw, err := os.ReadFile("../../config/gated/gazetteer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := location.ParseFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newMachine(t *testing.T) *Machine {
	t.Helper()
	m, err := NewMachine(repoBundle(t).Tool1, repoGazetteer(t), 7)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func itoa(i int) string { return strconv.Itoa(i) }

func fresh() incident.Projection {
	return incident.Projection{Reference: incident.Reference("1234567894"), State: incident.StateIntake, Version: 1}
}

// answer feeds input to the prompt the machine shows and applies the answer
// to the projection the way the event log would.
func answer(t *testing.T, m *Machine, p *incident.Projection, shown Kind, input string, hasPhone bool) Outcome {
	t.Helper()
	out, err := m.Answer(*p, shown, input, hasPhone)
	if err != nil {
		t.Fatalf("Answer(%s, %q): %v", shown, input, err)
	}
	if a := out.Answer; a != nil {
		switch a.Field {
		case incident.FieldLanguage:
			p.Language = a.Value
		case incident.FieldDanger:
			p.Danger = a.Value
		case incident.FieldCategory:
			p.Category = a.Value
		case incident.FieldAgeBand:
			p.AgeBand = a.Value
		case incident.FieldSpecificDanger:
			p.SpecificDanger = a.Value
		case incident.FieldWard:
			p.Ward = a.Value
		case incident.FieldLandmark:
			p.Landmark = a.Value
		}
		p.LastScreen = max(p.LastScreen, a.Screen)
	}
	return out
}

func TestMachineWalksEightScreens(t *testing.T) {
	m := newMachine(t)
	p := fresh()
	want := []struct {
		kind  Kind
		input string
	}{
		{KindLanguage, "1"}, {KindDanger, "2"}, {KindCategory, "4"}, {KindAgeBand, "3"},
		{KindSpecificDanger, "2"}, {KindWard, "1"}, {KindLandmark, "2"}, {KindCallback, "1"},
	}
	for i, w := range want {
		pr := m.Next(p, true)
		if pr.Kind != w.kind || pr.Screen != i+1 || pr.End {
			t.Fatalf("step %d: prompt %+v, want %s", i+1, pr, w.kind)
		}
		out := answer(t, m, &p, pr.Kind, w.input, true)
		if out.Answer == nil || out.Answer.Screen != i+1 {
			t.Fatalf("step %d: outcome %+v", i+1, out)
		}
	}
	if p.Language != "en" || p.Danger != "no" || p.Category != "poisoning" || p.AgeBand != "age_18_59" ||
		p.SpecificDanger != "no" || p.Ward != "w01" || p.Landmark != "w01_clinic" || p.LastScreen != 8 {
		t.Fatalf("projection after the walk: %+v", p)
	}
	done := m.Next(p, true)
	if done.Kind != KindComplete || !done.End || !strings.Contains(done.Text, "123 456 7894") {
		t.Fatalf("completion = %+v", done)
	}
}

func TestInvalidChoiceReprompts(t *testing.T) {
	m := newMachine(t)
	p := fresh()
	p.Language = "en"
	p.LastScreen = 1
	for _, bad := range []string{"", "9", "0", "x", "1 2", "-1", "01x", "99999999999999999999"} {
		out, err := m.Answer(p, KindDanger, bad, true)
		if err != nil {
			t.Fatal(err)
		}
		if out.Answer != nil || !out.Invalid {
			t.Errorf("input %q: %+v, want an invalid re-prompt", bad, out)
		}
	}
	pr := m.Reprompt(p, KindDanger, true)
	if pr.Kind != KindDanger || pr.Notice == "" {
		t.Fatalf("re-prompt = %+v", pr)
	}
	if out, _ := m.Answer(p, KindDanger, " 1 ", true); out.Answer == nil || out.Answer.Value != "yes" {
		t.Fatalf("padded digit: %+v", out)
	}
}

func TestSomewhereElseIsOutOfArea(t *testing.T) {
	m := newMachine(t)
	p := fresh()
	p.Language, p.Danger, p.Category, p.AgeBand, p.SpecificDanger, p.LastScreen = "en", "no", "trauma", "age_18_59", "no", 5
	ward := m.Next(p, true)
	last := ward.Options[len(ward.Options)-1]
	if last.Value != location.WardElsewhere {
		t.Fatalf("last ward option = %+v", last)
	}
	out := answer(t, m, &p, KindWard, itoa(last.Digit), true)
	if !out.OutOfArea || out.DispatchReady || out.Answer.Value != location.WardElsewhere {
		t.Fatalf("outcome = %+v", out)
	}
	end := m.Next(p, true)
	if end.Kind != KindOutOfArea || !end.End || !strings.Contains(end.Text, "999") || !strings.Contains(end.Text, "112") {
		t.Fatalf("out-of-area screen = %+v", end)
	}
}

func TestPaediatricDangerPrompt(t *testing.T) {
	m := newMachine(t)
	b := repoBundle(t)
	p := fresh()
	p.Language, p.Danger, p.Category, p.LastScreen = "en", "no", "acute_medical", 4
	p.AgeBand = "under_5"
	if got := m.Next(p, true).Text; got != b.Tool1.SpecificDanger.Prompts[config.ChildDangerKey]["en"] {
		t.Fatalf("child prompt = %q", got)
	}
	p.AgeBand = "age_18_59"
	if got := m.Next(p, true).Text; got != b.Tool1.SpecificDanger.Prompts["acute_medical"]["en"] {
		t.Fatalf("adult prompt = %q", got)
	}
	p.Category = "trauma"
	p.AgeBand = "under_5"
	if got := m.Next(p, true).Text; got != b.Tool1.SpecificDanger.Prompts["trauma"]["en"] {
		t.Fatalf("child trauma prompt = %q (paediatric signs belong to acute medical only)", got)
	}
}

func TestClassifyAfterScreen5(t *testing.T) {
	m := newMachine(t)
	p := fresh()
	for _, step := range []struct {
		k  Kind
		in string
	}{{KindLanguage, "1"}, {KindDanger, "1"}, {KindCategory, "1"}, {KindAgeBand, "1"}} {
		if out := answer(t, m, &p, step.k, step.in, true); out.Classify {
			t.Fatalf("%s classified early", step.k)
		}
	}
	if out := answer(t, m, &p, KindSpecificDanger, "2", true); !out.Classify {
		t.Fatal("screen 5 did not classify")
	}
}

func TestDispatchReadyAfterTriggerScreen(t *testing.T) {
	for trigger, wantAt := range map[int]Kind{7: KindLandmark, 8: KindCallback} {
		m, err := NewMachine(repoBundle(t).Tool1, repoGazetteer(t), trigger)
		if err != nil {
			t.Fatal(err)
		}
		p := fresh()
		p.Language, p.Danger, p.Category, p.AgeBand, p.SpecificDanger, p.LastScreen = "en", "no", "trauma", "age_18_59", "no", 5
		for _, step := range []struct {
			k  Kind
			in string
		}{{KindWard, "1"}, {KindLandmark, "1"}, {KindCallback, "1"}} {
			out := answer(t, m, &p, step.k, step.in, true)
			if out.DispatchReady != (step.k == wantAt) {
				t.Fatalf("trigger %d: %s DispatchReady=%v", trigger, step.k, out.DispatchReady)
			}
		}
	}
	for _, bad := range []int{0, 5, 6, 9} {
		if _, err := NewMachine(repoBundle(t).Tool1, repoGazetteer(t), bad); err == nil {
			t.Errorf("trigger screen %d accepted (location is known only after screen 7)", bad)
		}
	}
}

// Option 6 on screen 7 asks for up to 20 characters of free text, kept
// verbatim (a '*' included) for sealing and fuzzy matching.
func TestFreeTextLandmark(t *testing.T) {
	m := newMachine(t)
	p := fresh()
	p.Language, p.Danger, p.Category, p.AgeBand, p.SpecificDanger, p.Ward, p.LastScreen = "en", "no", "trauma", "age_18_59", "no", "w01", 6
	out := answer(t, m, &p, KindLandmark, "6", true)
	if out.Answer != nil || out.Next == nil || out.Next.Kind != KindLandmarkText {
		t.Fatalf("option 6: %+v", out)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", 21), "Kawempe ŋ market"} {
		if o, _ := m.Answer(p, KindLandmarkText, bad, true); o.Answer != nil || !o.Invalid {
			t.Errorf("free text %q accepted: %+v", bad, o)
		}
	}
	out = answer(t, m, &p, KindLandmarkText, "Mkt*near taxi", true)
	if out.Answer == nil || out.Answer.Value != incident.LandmarkOther || out.FreeText != "Mkt*near taxi" || !out.DispatchReady {
		t.Fatalf("free text: %+v", out)
	}
}

func TestCallbackNumber(t *testing.T) {
	m := newMachine(t)
	p := fresh()
	p.Language, p.Danger, p.Category, p.AgeBand, p.SpecificDanger, p.Ward, p.Landmark, p.LastScreen =
		"en", "no", "trauma", "age_18_59", "no", "w01", "w01_market", 7
	if pr := m.Next(p, true); pr.Kind != KindCallback || len(pr.Options) != 1 {
		t.Fatalf("callback prompt with a number = %+v", pr)
	}
	if pr := m.Next(p, false); pr.Kind != KindCallback || len(pr.Options) != 0 {
		t.Fatalf("withheld number prompt = %+v", pr)
	}
	if o, _ := m.Answer(p, KindCallback, "1", false); o.Answer != nil || !o.Invalid {
		t.Fatalf("'this number' accepted without a number: %+v", o)
	}
	o, _ := m.Answer(p, KindCallback, "0772 123 456", false)
	if o.Answer == nil || o.Answer.Value != incident.CallbackProvided || o.Callback == nil || o.Callback.E164() != "+256772123456" || !o.Complete {
		t.Fatalf("typed number: %+v", o)
	}
	if o, _ := m.Answer(p, KindCallback, "12", true); o.Answer != nil || !o.Invalid {
		t.Fatalf("invalid number accepted: %+v", o)
	}
	o, _ = m.Answer(p, KindCallback, "1", true)
	if o.Answer == nil || o.Answer.Value != incident.CallbackSameNumber || o.Callback != nil {
		t.Fatalf("same number: %+v", o)
	}
}

// Build Prompt §16: a USSD screen over the operator limit, in either
// language, fails the build. Every screen is rendered at its worst case
// (each ward's landmark menu, the invalid-choice notice, the longest
// reference and first-aid line) for every configured network limit.
func TestEveryRenderedScreenFitsLimitAndGSM7(t *testing.T) {
	b := repoBundle(t)
	m := newMachine(t)
	limits := map[string]int{"default": b.Intake.DefaultScreenCharLimit}
	for net, l := range b.Intake.ScreenCharLimits {
		limits[net] = l
	}
	prompts := m.AllPrompts()
	if len(prompts) < 20 {
		t.Fatalf("only %d prompts enumerated", len(prompts))
	}
	for _, pr := range prompts {
		text, _ := RenderUSSD(pr)
		if !config.IsGSM7(text) {
			t.Errorf("%s/%s: non-GSM characters: %q", pr.Kind, pr.Lang, text)
		}
		for net, l := range limits {
			if n := utf8.RuneCountInString(text); n > l {
				t.Errorf("%s/%s on %s: %d characters > %d:\n%s", pr.Kind, pr.Lang, net, n, l, text)
			}
		}
	}
	if err := m.CheckScreens(limits); err != nil {
		t.Fatalf("CheckScreens: %v", err)
	}
	tiny := map[string]int{"default": 20}
	if err := m.CheckScreens(tiny); err == nil {
		t.Fatal("CheckScreens accepted a 20-character limit")
	}
}

func TestNewMachineRejectsTooManyWards(t *testing.T) {
	g := repoGazetteer(t)
	for len(g.Wards) < 9 {
		w := g.Wards[0]
		w.ID = "zz" + itoa(len(g.Wards))
		g.Wards = append(g.Wards, w)
	}
	if _, err := NewMachine(repoBundle(t).Tool1, g, 7); err == nil {
		t.Fatal("9 wards plus Somewhere else accepted on a single-digit menu")
	}
}

// The catalogue's option values must be exactly the signed triage bundle's
// input domain, so no answer can fall outside what the rules classify.
func TestCatalogueMatchesTriageDomain(t *testing.T) {
	e := repoTriage(t)
	cat := repoBundle(t).Tool1
	if err := CheckTriageDomain(cat, e.Values); err != nil {
		t.Fatalf("repository catalogue vs bundle: %v", err)
	}
	cat.Category.Options = append(cat.Category.Options, config.Option{Value: "snake_bite", Label: config.Text{"en": "x", "lg": "x"}})
	if err := CheckTriageDomain(cat, e.Values); err == nil || !strings.Contains(err.Error(), "snake_bite") {
		t.Fatalf("extra category accepted: %v", err)
	}
}
