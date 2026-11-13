package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Responder is gated/responder.yaml: the responder's USSD and voice menu
// copy, and the decline reasons (decision 6, Build Prompt §10.5): a closed
// list per role whose last option is Other, no free text, asked only once
// the amendment for decision 6 is approved. Status "placeholder" (and any
// PENDING marker) blocks production.
type Responder struct {
	Status         string                     `yaml:"status"`
	Languages      []string                   `yaml:"languages"`
	DeclineReasons map[string][]DeclineReason `yaml:"decline_reasons"`
	Menu           map[string]Text            `yaml:"menu"`
}

// DeclineReason is one option of a role's closed list.
type DeclineReason struct {
	Code  string `yaml:"code"`
	Label Text   `yaml:"label"`
}

// Responder menu texts (keys of Responder.Menu) and the placeholders each
// may carry.
var responderMenu = map[string][]string{
	"not_registered": nil, "alert": {"ref", "colour", "category"}, "accept": nil, "decline": nil, "availability": nil,
	"no_alert": nil, "accepted": {"ref", "landmark", "ward"}, "assigned": {"ref", "landmark", "ward"}, "already_taken": nil,
	"already_assigned": nil, "no_longer_needed": nil, "declined": {"ref"}, "decline_why": nil, "switch_on": nil, "switch_off": nil,
	"available_until": {"until"}, "switched_off": nil, "invalid": nil,
}

// requiredPlaceholders must appear in every language of a menu text.
var requiredPlaceholders = map[string][]string{"accepted": {"ref"}, "assigned": {"ref"}, "alert": {"ref"}, "available_until": {"until"}}

// Validate checks the reasons and the menu.
func (r Responder) Validate() error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if r.Status != "placeholder" && r.Status != "approved" {
		bad("status %q (want placeholder or approved)", r.Status)
	}
	if !slices.Contains(r.Languages, "en") {
		bad("languages must include en (the fallback)")
	}
	for _, role := range []string{"vht", "transporter"} {
		reasons := r.DeclineReasons[role]
		if len(reasons) < 2 || len(reasons) > maxMenuOptions-2 {
			bad("decline_reasons.%s: %d options (want 2-%d)", role, len(reasons), maxMenuOptions-2)
			continue
		}
		if reasons[len(reasons)-1].Code != "other" {
			bad("decline_reasons.%s: the last option must be other", role)
		}
		for i, x := range reasons {
			if !optionValue.MatchString(x.Code) || slices.ContainsFunc(reasons[:i], func(y DeclineReason) bool { return y.Code == x.Code }) {
				bad("decline_reasons.%s: %q must be a unique code", role, x.Code)
			}
			for _, lang := range r.Languages {
				if strings.TrimSpace(x.Label[lang]) == "" {
					bad("decline_reasons.%s.%s: missing %s", role, x.Code, lang)
				}
			}
		}
	}
	for role := range r.DeclineReasons {
		if role != "vht" && role != "transporter" {
			bad("decline_reasons: unknown role %s", role)
		}
	}
	for name := range r.Menu {
		if _, ok := responderMenu[name]; !ok {
			bad("menu.%s: unknown text", name)
		}
	}
	for _, name := range slices.Sorted(mapKeys(responderMenu)) {
		for _, lang := range r.Languages {
			text := r.Menu[name][lang]
			if strings.TrimSpace(text) == "" {
				bad("menu.%s/%s is missing", name, lang)
				continue
			}
			for _, m := range smsPlaceholder.FindAllStringSubmatch(text, -1) {
				if !slices.Contains(responderMenu[name], m[1]) {
					bad("menu.%s/%s: {%s} is not allowed", name, lang, m[1])
				}
			}
			for _, p := range requiredPlaceholders[name] {
				if !strings.Contains(text, "{"+p+"}") {
					bad("menu.%s/%s must carry {%s}", name, lang, p)
				}
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("responder: " + strings.Join(problems, "; "))
	}
	return nil
}

// Text returns a menu text in lang (English if lang has none).
func (r Responder) Text(name, lang string) string {
	if t := r.Menu[name][lang]; t != "" {
		return t
	}
	return r.Menu[name]["en"]
}

// texts returns every responder text as name/lang/text triples.
func (r Responder) texts() [][3]string {
	var out [][3]string
	for _, name := range slices.Sorted(mapKeys(r.Menu)) {
		for _, lang := range slices.Sorted(mapKeys(r.Menu[name])) {
			out = append(out, [3]string{"responder.menu." + name, lang, r.Menu[name][lang]})
		}
	}
	for _, role := range slices.Sorted(mapKeys(r.DeclineReasons)) {
		for _, x := range r.DeclineReasons[role] {
			for _, lang := range slices.Sorted(mapKeys(x.Label)) {
				out = append(out, [3]string{"responder.decline_reasons." + role + "." + x.Code, lang, x.Label[lang]})
			}
		}
	}
	return out
}
