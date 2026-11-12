package intake

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

// Kind names what the caller is being asked or told. The same kinds drive
// USSD (rendered text) and IVR (recorded prompts), so the two channels are
// functionally identical (Engineering Guide §4).
type Kind string

const (
	KindLanguage       Kind = "language"        // screen 1
	KindDanger         Kind = "danger"          // screen 2
	KindCategory       Kind = "category"        // screen 3
	KindAgeBand        Kind = "age_band"        // screen 4
	KindSpecificDanger Kind = "specific_danger" // screen 5
	KindWard           Kind = "ward"            // screen 6
	KindLandmark       Kind = "landmark"        // screen 7
	KindLandmarkText   Kind = "landmark_text"   // screen 7, option 6: free text
	KindCallback       Kind = "callback"        // screen 8
	KindOutOfArea      Kind = "out_of_area"     // end: 999 or 112
	KindComplete       Kind = "complete"        // end: help is coming
)

// MaxLandmarkText is the free-text landmark limit (Build Prompt §10.1).
const MaxLandmarkText = 20

// otherLandmarkDigit is the fixed free-text option on screen 7.
const otherLandmarkDigit = 6

// MenuOption is one numbered choice.
type MenuOption struct {
	Digit int
	Value string
	Label string
}

// Prompt is one screen, resolved to a language.
type Prompt struct {
	Kind    Kind
	Screen  int
	Lang    string
	Text    string
	Notice  string // the invalid-choice message on a re-prompt
	Options []MenuOption
	End     bool
	// Key selects the recorded IVR prompt where a kind has variants: the
	// danger-question key on screen 5, the ward on screen 7, the category on
	// the completion screen.
	Key string
}

// Outcome is the result of one answer.
type Outcome struct {
	// Answer is the screen.answered payload to record; nil when nothing is
	// recorded (an invalid input, or option 6 before its text).
	Answer *incident.ScreenAnswer
	// FreeText is a typed landmark to seal (Answer.Value is "other").
	FreeText string
	// Callback is a typed callback number to seal (Answer.Value "provided").
	Callback *pii.Phone
	// Next overrides the next prompt (the free-text sub-prompt of screen 7).
	Next *Prompt
	// Invalid asks for the same screen again with a notice.
	Invalid bool
	// Classify: screen 5 answered, severity can be computed.
	Classify bool
	// DispatchReady: the trigger screen answered inside the area (T2).
	DispatchReady bool
	// OutOfArea: "Somewhere else" chosen at screen 6.
	OutOfArea bool
	// Complete: screen 8 answered.
	Complete bool
}

// ErrUnknownKind reports an answer to a prompt kind that takes no input.
var ErrUnknownKind = errors.New("intake: prompt kind takes no answer")

// Machine is the Tool 1 state machine. It is pure: given the incident's
// projection and the prompt last shown, it decides what to record and what
// to ask next. Every answer is persisted by the caller the moment it is
// given (Guide §4), so an abandoned session still leaves its record.
type Machine struct {
	cat     config.Tool1
	gaz     location.File
	trigger int
}

// NewMachine validates the catalogue against the gazetteer and the T2
// trigger screen (7 or 8: location is known only after screen 7).
func NewMachine(cat config.Tool1, gaz location.File, triggerScreen int) (*Machine, error) {
	if triggerScreen != 7 && triggerScreen != 8 {
		return nil, fmt.Errorf("intake: dispatch trigger screen %d (want 7 or 8)", triggerScreen)
	}
	if len(gaz.Wards) == 0 || len(gaz.Wards)+1 > 9 {
		return nil, fmt.Errorf("intake: %d wards plus Somewhere else do not fit a single-digit menu", len(gaz.Wards))
	}
	for _, w := range gaz.Wards {
		if len(gaz.Menu(w.ID)) == 0 {
			return nil, fmt.Errorf("intake: ward %s has no menu landmarks", w.ID)
		}
	}
	return &Machine{cat: cat, gaz: gaz, trigger: triggerScreen}, nil
}

func (m *Machine) lang(p incident.Projection) string {
	if p.Language != "" {
		return p.Language
	}
	return "en"
}

func pick(t config.Text, lang string) string {
	if s := t[lang]; s != "" {
		return s
	}
	return t["en"]
}

func names(n location.Names, lang string) string {
	if lang == "lg" && n.LG != "" {
		return n.LG
	}
	return n.EN
}

func menu(opts []config.Option, lang string) []MenuOption {
	out := make([]MenuOption, len(opts))
	for i, o := range opts {
		out[i] = MenuOption{Digit: i + 1, Value: o.Value, Label: pick(o.Label, lang)}
	}
	return out
}

func (m *Machine) dangerKey(p incident.Projection) string {
	if p.Category == "acute_medical" {
		for _, o := range m.cat.AgeBand.Options {
			if o.Value == p.AgeBand && o.Child {
				if _, ok := m.cat.SpecificDanger.Prompts[config.ChildDangerKey]; ok {
					return config.ChildDangerKey
				}
			}
		}
	}
	return p.Category
}

// prompt builds the prompt of kind for projection p.
func (m *Machine) prompt(p incident.Projection, k Kind, hasPhone bool) Prompt {
	lang := m.lang(p)
	pr := Prompt{Kind: k, Lang: lang}
	switch k {
	case KindLanguage:
		pr.Screen, pr.Lang, pr.Text = 1, "*", m.cat.Language.Prompt
		for i, o := range m.cat.Language.Options {
			pr.Options = append(pr.Options, MenuOption{Digit: i + 1, Value: o.Value, Label: pick(o.Label, o.Value)})
		}
	case KindDanger:
		pr.Screen, pr.Text, pr.Options = 2, pick(m.cat.Danger.Prompt, lang), menu(m.cat.Danger.Options, lang)
	case KindCategory:
		pr.Screen, pr.Text, pr.Options = 3, pick(m.cat.Category.Prompt, lang), menu(m.cat.Category.Options, lang)
	case KindAgeBand:
		pr.Screen, pr.Text, pr.Options = 4, pick(m.cat.AgeBand.Prompt, lang), menu(m.cat.AgeBand.Options, lang)
	case KindSpecificDanger:
		pr.Key = m.dangerKey(p)
		pr.Screen, pr.Text = 5, pick(m.cat.SpecificDanger.Prompts[pr.Key], lang)
		pr.Options = menu(m.cat.SpecificDanger.Options, lang)
	case KindWard:
		pr.Screen, pr.Text = 6, pick(m.cat.Ward.Prompt, lang)
		for i, w := range m.gaz.Wards {
			pr.Options = append(pr.Options, MenuOption{Digit: i + 1, Value: w.ID, Label: names(w.Name, lang)})
		}
		pr.Options = append(pr.Options, MenuOption{Digit: len(m.gaz.Wards) + 1, Value: location.WardElsewhere,
			Label: pick(m.cat.Ward.ElsewhereLabel, lang)})
	case KindLandmark:
		pr.Screen, pr.Text, pr.Key = 7, pick(m.cat.Landmark.Prompt, lang), p.Ward
		for _, l := range m.gaz.Menu(p.Ward) {
			pr.Options = append(pr.Options, MenuOption{Digit: l.Menu, Value: l.ID, Label: names(l.Name, lang)})
		}
		pr.Options = append(pr.Options, MenuOption{Digit: otherLandmarkDigit, Value: location.LandmarkOther,
			Label: pick(m.cat.Landmark.OtherLabel, lang)})
	case KindLandmarkText:
		pr.Screen, pr.Text, pr.Key = 7, pick(m.cat.Landmark.OtherPrompt, lang), p.Ward
	case KindCallback:
		pr.Screen = 8
		if hasPhone {
			pr.Text = pick(m.cat.Callback.Prompt, lang)
			pr.Options = []MenuOption{{Digit: 1, Value: incident.CallbackSameNumber, Label: pick(m.cat.Callback.SameNumberLabel, lang)}}
		} else {
			pr.Text = pick(m.cat.Callback.PromptWithheld, lang)
		}
	case KindOutOfArea:
		pr.Screen, pr.End, pr.Text = 6, true, pick(m.cat.Messages.OutOfArea, lang)
	case KindComplete:
		pr.Screen, pr.End, pr.Key = 8, true, p.Category
		pr.Text = strings.NewReplacer("{ref}", p.Reference.Spoken(),
			"{first_aid}", pick(m.cat.Messages.FirstAid[p.Category], lang)).Replace(pick(m.cat.Messages.HelpComing, lang))
	}
	return pr
}

// Next returns the first unanswered screen, or the closing message.
func (m *Machine) Next(p incident.Projection, hasPhone bool) Prompt {
	var k Kind
	switch {
	case p.Ward == location.WardElsewhere:
		k = KindOutOfArea
	case p.Language == "":
		k = KindLanguage
	case p.Danger == "":
		k = KindDanger
	case p.Category == "":
		k = KindCategory
	case p.AgeBand == "":
		k = KindAgeBand
	case p.SpecificDanger == "":
		k = KindSpecificDanger
	case p.Ward == "":
		k = KindWard
	case p.Landmark == "":
		k = KindLandmark
	case p.LastScreen < 8:
		k = KindCallback
	default:
		k = KindComplete
	}
	return m.prompt(p, k, hasPhone)
}

// Reprompt returns the prompt of kind again with the invalid-choice notice.
func (m *Machine) Reprompt(p incident.Projection, k Kind, hasPhone bool) Prompt {
	pr := m.prompt(p, k, hasPhone)
	pr.Notice = pick(m.cat.Messages.Invalid, m.lang(p))
	return pr
}

func digit(input string) (int, bool) {
	s := strings.TrimSpace(input)
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	return int(s[0] - '0'), true
}

func fieldOf(k Kind) string {
	switch k {
	case KindLanguage:
		return incident.FieldLanguage
	case KindDanger:
		return incident.FieldDanger
	case KindCategory:
		return incident.FieldCategory
	case KindAgeBand:
		return incident.FieldAgeBand
	case KindSpecificDanger:
		return incident.FieldSpecificDanger
	case KindWard:
		return incident.FieldWard
	case KindLandmark, KindLandmarkText:
		return incident.FieldLandmark
	}
	return incident.FieldCallback
}

// Answer interprets input given to the prompt of kind shown.
func (m *Machine) Answer(p incident.Projection, shown Kind, input string, hasPhone bool) (Outcome, error) {
	invalid := Outcome{Invalid: true}
	var out Outcome
	switch shown {
	case KindLanguage, KindDanger, KindCategory, KindAgeBand, KindSpecificDanger, KindWard, KindLandmark:
		d, ok := digit(input)
		if !ok {
			return invalid, nil
		}
		pr := m.prompt(p, shown, hasPhone)
		var chosen *MenuOption
		for i := range pr.Options {
			if pr.Options[i].Digit == d {
				chosen = &pr.Options[i]
			}
		}
		if chosen == nil {
			return invalid, nil
		}
		if shown == KindLandmark && chosen.Value == location.LandmarkOther {
			next := m.prompt(p, KindLandmarkText, hasPhone)
			return Outcome{Next: &next}, nil
		}
		out.Answer = &incident.ScreenAnswer{Screen: pr.Screen, Field: fieldOf(shown), Value: chosen.Value}
	case KindLandmarkText:
		text := strings.TrimSpace(input)
		if text == "" || utf8.RuneCountInString(text) > MaxLandmarkText || !config.IsGSM7(text) {
			return invalid, nil
		}
		out.Answer = &incident.ScreenAnswer{Screen: 7, Field: incident.FieldLandmark, Value: incident.LandmarkOther}
		out.FreeText = text
	case KindCallback:
		if hasPhone && strings.TrimSpace(input) == "1" {
			out.Answer = &incident.ScreenAnswer{Screen: 8, Field: incident.FieldCallback, Value: incident.CallbackSameNumber}
			break
		}
		ph, err := pii.ParsePhone(input)
		if err != nil {
			return invalid, nil //nolint:nilerr // an unparseable number is caller input: ask again
		}
		out.Answer = &incident.ScreenAnswer{Screen: 8, Field: incident.FieldCallback, Value: incident.CallbackProvided}
		out.Callback = &ph
	default:
		return Outcome{}, fmt.Errorf("%w: %s", ErrUnknownKind, shown)
	}
	a := out.Answer
	out.Classify = a.Screen == 5
	out.OutOfArea = a.Field == incident.FieldWard && a.Value == location.WardElsewhere
	out.DispatchReady = a.Screen == m.trigger
	out.Complete = a.Screen == 8
	return out, nil
}

// AllPrompts enumerates every screen at its worst case, in every language:
// each danger question, each ward's landmark menu, each first-aid line, with
// and without the invalid-choice notice. It feeds the screen-length gate.
func (m *Machine) AllPrompts() []Prompt {
	var out []Prompt
	add := func(p incident.Projection, k Kind, hasPhone bool) {
		pr := m.prompt(p, k, hasPhone)
		out = append(out, pr)
		if k != KindOutOfArea && k != KindComplete {
			out = append(out, m.Reprompt(p, k, hasPhone))
		}
	}
	add(incident.Projection{}, KindLanguage, true)
	for _, lang := range m.cat.Languages {
		base := incident.Projection{Language: lang, Reference: "9999999999"}
		for _, k := range []Kind{KindDanger, KindCategory, KindAgeBand, KindWard, KindLandmarkText, KindOutOfArea} {
			add(base, k, true)
		}
		add(base, KindCallback, true)
		add(base, KindCallback, false)
		for _, c := range m.cat.Category.Options {
			for _, a := range m.cat.AgeBand.Options {
				p := base
				p.Category, p.AgeBand = c.Value, a.Value
				add(p, KindSpecificDanger, true)
			}
			p := base
			p.Category = c.Value
			add(p, KindComplete, true)
		}
		for _, w := range m.gaz.Wards {
			p := base
			p.Ward = w.ID
			add(p, KindLandmark, true)
		}
	}
	return out
}

// CheckScreens renders every prompt and fails if any exceeds a limit (per
// network code, plus "default") or leaves the GSM 03.38 basic table (Build
// Prompt §16). intake-gw refuses to start if it fails.
func (m *Machine) CheckScreens(limits map[string]int) error {
	var problems []string
	for _, pr := range m.AllPrompts() {
		text, _ := RenderUSSD(pr)
		if !config.IsGSM7(text) {
			problems = append(problems, fmt.Sprintf("%s/%s: non-GSM characters", pr.Kind, pr.Lang))
		}
		n := utf8.RuneCountInString(text)
		for net, l := range limits {
			if n > l {
				problems = append(problems, fmt.Sprintf("%s/%s key %q on %s: %d > %d characters", pr.Kind, pr.Lang, pr.Key, net, n, l))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("intake: screens: " + strings.Join(problems, "; "))
	}
	return nil
}
