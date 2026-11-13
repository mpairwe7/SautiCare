package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Text is caller-facing copy keyed by language ("en", "lg").
type Text map[string]string

// Option is one menu choice: the coded value recorded in screen.answered and
// its label in each language. Child marks age bands under 18 (the paediatric
// danger question, Engineering Guide §5).
type Option struct {
	Value string `yaml:"value"`
	Label Text   `yaml:"label"`
	Child bool   `yaml:"child"`
}

// Question is a menu screen.
type Question struct {
	Prompt  Text     `yaml:"prompt"`
	Options []Option `yaml:"options"`
}

// LanguageScreen is screen 1, shown before the language is known, so its
// prompt is one bilingual text.
type LanguageScreen struct {
	Prompt  string   `yaml:"prompt"`
	Options []Option `yaml:"options"`
}

// SpecificDangerScreen is screen 5: one danger question per category, plus
// "acute_medical_child" for acute medical illness under 18.
type SpecificDangerScreen struct {
	Prompts map[string]Text `yaml:"prompts"`
	Options []Option        `yaml:"options"`
}

// WardScreen is screen 6; the wards come from the gazetteer.
type WardScreen struct {
	Prompt         Text `yaml:"prompt"`
	ElsewhereLabel Text `yaml:"elsewhere_label"`
}

// LandmarkScreen is screen 7; the landmarks come from the gazetteer, and the
// last option asks for free text (at most 20 characters).
type LandmarkScreen struct {
	Prompt      Text `yaml:"prompt"`
	OtherLabel  Text `yaml:"other_label"`
	OtherPrompt Text `yaml:"other_prompt"`
}

// CallbackScreen is screen 8. PromptWithheld is used when the session has no
// number to offer as "this number".
type CallbackScreen struct {
	Prompt          Text `yaml:"prompt"`
	PromptWithheld  Text `yaml:"prompt_withheld"`
	SameNumberLabel Text `yaml:"same_number_label"`
}

// Messages are the non-question screens. HelpComing carries {ref} (the spoken
// incident reference) and {first_aid} (the Tool 1 first-aid line for the
// category); OutOfArea gives 999 or 112 (decision 3). DispatchSuspended
// replaces HelpComing while dispatch is suspended (O6): it carries {ref},
// and needs the Medical Director's and PI's approval before production.
type Messages struct {
	OutOfArea         Text            `yaml:"out_of_area"`
	HelpComing        Text            `yaml:"help_coming"`
	DispatchSuspended Text            `yaml:"dispatch_suspended"`
	Invalid           Text            `yaml:"invalid"`
	FirstAid          map[string]Text `yaml:"first_aid"`
}

// Tool1 is the caller-facing catalogue for the eight Tool 1 screens (Build
// Prompt §10.1). The wording, categories, age bands and danger questions are
// protocol content: until Data Collection Tool 1 is received they are
// placeholders, and Status "placeholder" blocks production.
type Tool1 struct {
	Status         string               `yaml:"status"`
	Languages      []string             `yaml:"languages"`
	Language       LanguageScreen       `yaml:"language"`
	Danger         Question             `yaml:"danger"`
	Category       Question             `yaml:"category"`
	AgeBand        Question             `yaml:"age_band"`
	SpecificDanger SpecificDangerScreen `yaml:"specific_danger"`
	Ward           WardScreen           `yaml:"ward"`
	Landmark       LandmarkScreen       `yaml:"landmark"`
	Callback       CallbackScreen       `yaml:"callback"`
	Messages       Messages             `yaml:"messages"`
}

// ChildDangerKey selects the paediatric danger question.
const ChildDangerKey = "acute_medical_child"

// maxMenuOptions keeps every menu answerable with one keypad digit.
const maxMenuOptions = 9

var optionValue = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// placeholders are substituted at render time and are not caller text.
var renderPlaceholders = strings.NewReplacer("{ref}", "", "{first_aid}", "")

// Texts lists every catalogue text as name/lang/text triples.
func (t Tool1) Texts() [][3]string {
	var out [][3]string
	add := func(name string, tx Text) {
		for _, lang := range slices.Sorted(mapKeys(tx)) {
			out = append(out, [3]string{name, lang, tx[lang]})
		}
	}
	out = append(out, [3]string{"language.prompt", "*", t.Language.Prompt})
	addOptions := func(name string, opts []Option) {
		for _, o := range opts {
			add(name+"."+o.Value, o.Label)
		}
	}
	addOptions("language", t.Language.Options)
	for _, q := range []struct {
		name string
		q    Question
	}{{"danger", t.Danger}, {"category", t.Category}, {"age_band", t.AgeBand}} {
		add(q.name+".prompt", q.q.Prompt)
		addOptions(q.name, q.q.Options)
	}
	for _, k := range slices.Sorted(mapKeys(t.SpecificDanger.Prompts)) {
		add("specific_danger.prompts."+k, t.SpecificDanger.Prompts[k])
	}
	addOptions("specific_danger", t.SpecificDanger.Options)
	add("ward.prompt", t.Ward.Prompt)
	add("ward.elsewhere_label", t.Ward.ElsewhereLabel)
	add("landmark.prompt", t.Landmark.Prompt)
	add("landmark.other_label", t.Landmark.OtherLabel)
	add("landmark.other_prompt", t.Landmark.OtherPrompt)
	add("callback.prompt", t.Callback.Prompt)
	add("callback.prompt_withheld", t.Callback.PromptWithheld)
	add("callback.same_number_label", t.Callback.SameNumberLabel)
	add("messages.out_of_area", t.Messages.OutOfArea)
	add("messages.help_coming", t.Messages.HelpComing)
	add("messages.dispatch_suspended", t.Messages.DispatchSuspended)
	add("messages.invalid", t.Messages.Invalid)
	for _, k := range slices.Sorted(mapKeys(t.Messages.FirstAid)) {
		add("messages.first_aid."+k, t.Messages.FirstAid[k])
	}
	return out
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func values(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Value
	}
	return out
}

// Validate checks that the catalogue is complete and renderable: every text
// in every language, coded and unique option values, the danger answers the
// triage rule reads, a danger question and first-aid line per category, the
// reference and the first-aid line on the completion screen, 999 and 112 on the out-of-area screen,
// and GSM 03.38 basic characters only.
func (t Tool1) Validate() error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if t.Status != "placeholder" && t.Status != "approved" {
		bad("status %q (want placeholder or approved)", t.Status)
	}
	if !slices.Contains(t.Languages, "en") {
		bad("languages must include en (the fallback)")
	}
	checkText := func(name string, tx Text) {
		for _, lang := range t.Languages {
			if strings.TrimSpace(tx[lang]) == "" {
				bad("%s: missing %s", name, lang)
			}
		}
		for lang := range tx {
			if !slices.Contains(t.Languages, lang) {
				bad("%s: unknown language %s", name, lang)
			}
		}
	}
	checkOptions := func(name string, opts []Option, want []string) {
		if len(opts) == 0 || len(opts) > maxMenuOptions {
			bad("%s: %d options (want 1-%d)", name, len(opts), maxMenuOptions)
		}
		for i, o := range opts {
			if !optionValue.MatchString(o.Value) || slices.Contains(values(opts[:i]), o.Value) {
				bad("%s: option value %q must be a unique code", name, o.Value)
			}
			checkText(name+"."+o.Value, o.Label)
		}
		if want != nil && !slices.Equal(slices.Sorted(slices.Values(values(opts))), slices.Sorted(slices.Values(want))) {
			bad("%s: options %v, want exactly %v", name, values(opts), want)
		}
	}
	if strings.TrimSpace(t.Language.Prompt) == "" {
		bad("language.prompt is empty")
	}
	checkOptions("language", t.Language.Options, t.Languages)
	checkText("danger.prompt", t.Danger.Prompt)
	checkOptions("danger", t.Danger.Options, []string{"yes", "no", "unknown"})
	checkText("category.prompt", t.Category.Prompt)
	checkOptions("category", t.Category.Options, nil)
	checkText("age_band.prompt", t.AgeBand.Prompt)
	checkOptions("age_band", t.AgeBand.Options, nil)
	checkOptions("specific_danger", t.SpecificDanger.Options, []string{"yes", "no"})
	cats := values(t.Category.Options)
	childBand := slices.ContainsFunc(t.AgeBand.Options, func(o Option) bool { return o.Child })
	for _, c := range cats {
		checkText("specific_danger.prompts."+c, t.SpecificDanger.Prompts[c])
		checkText("messages.first_aid."+c, t.Messages.FirstAid[c])
	}
	if childBand && slices.Contains(cats, "acute_medical") {
		checkText("specific_danger.prompts."+ChildDangerKey, t.SpecificDanger.Prompts[ChildDangerKey])
	}
	for k := range t.SpecificDanger.Prompts {
		if !slices.Contains(cats, k) && k != ChildDangerKey {
			bad("specific_danger.prompts.%s: no such category", k)
		}
	}
	for _, x := range []struct {
		name string
		tx   Text
	}{
		{"ward.prompt", t.Ward.Prompt}, {"ward.elsewhere_label", t.Ward.ElsewhereLabel},
		{"landmark.prompt", t.Landmark.Prompt}, {"landmark.other_label", t.Landmark.OtherLabel},
		{"landmark.other_prompt", t.Landmark.OtherPrompt}, {"callback.prompt", t.Callback.Prompt},
		{"callback.prompt_withheld", t.Callback.PromptWithheld}, {"callback.same_number_label", t.Callback.SameNumberLabel},
		{"messages.out_of_area", t.Messages.OutOfArea}, {"messages.help_coming", t.Messages.HelpComing},
		{"messages.dispatch_suspended", t.Messages.DispatchSuspended}, {"messages.invalid", t.Messages.Invalid},
	} {
		checkText(x.name, x.tx)
	}
	for _, lang := range t.Languages {
		if !strings.Contains(t.Messages.DispatchSuspended[lang], "{ref}") {
			bad("messages.dispatch_suspended/%s must carry {ref}", lang)
		}
		for _, ph := range []string{"{ref}", "{first_aid}"} {
			if !strings.Contains(t.Messages.HelpComing[lang], ph) {
				bad("messages.help_coming/%s must carry %s", lang, ph)
			}
		}
		if oa := t.Messages.OutOfArea[lang]; !strings.Contains(oa, "999") || !strings.Contains(oa, "112") {
			bad("messages.out_of_area/%s must give 999 and 112", lang)
		}
	}
	for _, e := range t.Texts() {
		if !IsGSM7(renderPlaceholders.Replace(e[2])) {
			bad("%s/%s: characters outside GSM 03.38", e[0], e[1])
		}
	}
	if len(problems) > 0 {
		return errors.New("config: tool1: " + strings.Join(problems, "; "))
	}
	return nil
}
