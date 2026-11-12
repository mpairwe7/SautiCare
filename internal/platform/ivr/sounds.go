package ivr

import (
	"strconv"

	"sautihealth.com/sauti/internal/intake"
)

// Sound IDs are derived from the prompt, so the recording team's catalogue
// (telephony/prompts/catalogue.yaml) follows the Tool 1 catalogue and the
// gazetteer without extra configuration. Every ID resolves to a file under
// Asterisk's sounds directory, recorded by human speakers in each language
// (Guide §4); nothing is synthesised and nothing is recorded from callers.
func base(lang string) string { return "sauti/" + lang + "/" }

// Sounds returns the sound IDs that voice a prompt, in order.
func Sounds(pr intake.Prompt) []string {
	b := base(pr.Lang)
	var out []string
	if pr.Notice != "" {
		if pr.Lang == "*" { // before a language is chosen: the bilingual notice
			out = append(out, "sauti/invalid")
		} else {
			out = append(out, b+"invalid")
		}
	}
	switch pr.Kind {
	case intake.KindLanguage:
		out = append(out, "sauti/language")
		for _, o := range pr.Options {
			out = append(out, base(o.Value)+"press-"+strconv.Itoa(o.Digit), base(o.Value)+"language-"+o.Value)
		}
		return out
	case intake.KindSpecificDanger:
		out = append(out, b+"specific_danger-"+pr.Key)
	case intake.KindCallback:
		if len(pr.Options) == 0 {
			out = append(out, b+"callback-withheld")
		} else {
			out = append(out, b+"callback")
		}
	case intake.KindComplete:
		out = append(out, b+"complete")
		for _, r := range pr.Reference {
			if r >= '0' && r <= '9' {
				out = append(out, "sauti/digits/"+string(r))
			}
		}
		return append(out, b+"first_aid-"+pr.Key)
	default:
		out = append(out, b+string(pr.Kind))
	}
	for _, o := range pr.Options {
		out = append(out, b+"press-"+strconv.Itoa(o.Digit), b+string(pr.Kind)+"-"+o.Value)
	}
	return out
}

// InternalError is played when intake cannot take the call.
func InternalError(lang string) []string {
	if lang == "" || lang == "*" {
		lang = "en"
	}
	return []string{base(lang) + "internal_error"}
}
