package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SMS template names (Build Prompt §10.4, §16; hard constraint 6; decision 1).
const (
	SMSAlert                = "alert"                 // responder: protocol mode, no location
	SMSAlertAmended         = "alert_amended"         // responder: with ward and landmark (decision 1, amendment)
	SMSStandDown            = "stand_down"            // responder: no longer needed
	SMSAvailabilityExpiring = "availability_expiring" // responder: the 8-hour period ends soon
	SMSHelperOnTheWay       = "helper_on_the_way"     // caller: no responder name or number
	SMSForwardedToFacility  = "forwarded_to_facility" // caller: the centre's name and public number
	SMSFacilityForward      = "facility_forward"      // health centre: incident number, colour, category, ward, landmark
)

// smsPlaceholders are the only values each template may carry. There is no
// placeholder for a name, a clinical answer or the caller's number, and
// SMSValues has no field for them.
var smsPlaceholders = map[string][]string{
	SMSAlert:                {"ref", "colour", "category"},
	SMSAlertAmended:         {"ref", "colour", "category", "ward", "landmark"},
	SMSStandDown:            {"ref"},
	SMSAvailabilityExpiring: {"time"},
	SMSHelperOnTheWay:       {"ref"},
	SMSForwardedToFacility:  {"ref", "facility_name", "facility_phone"},
	SMSFacilityForward:      {"ref", "colour", "category", "ward", "landmark"},
}

// SMSResponderTemplates are the templates sent to responders.
func SMSResponderTemplates() []string {
	return []string{SMSAlert, SMSAlertAmended, SMSStandDown, SMSAvailabilityExpiring}
}

// SMSValues are what a template may show. Ref is the spoken incident number.
type SMSValues struct {
	Ref, Colour, Category, Ward, Landmark, Time, FacilityName, FacilityPhone string
}

func (v SMSValues) lookup(key string) string {
	return map[string]string{"ref": v.Ref, "colour": v.Colour, "category": v.Category, "ward": v.Ward, "landmark": v.Landmark,
		"time": v.Time, "facility_name": v.FacilityName, "facility_phone": v.FacilityPhone}[key]
}

// worstCaseSMS is long values for the length check.
var worstCaseSMS = SMSValues{Ref: "123 456 7890", Colour: "yellow", Category: strings.Repeat("c", 24), Ward: strings.Repeat("w", 24),
	Landmark: strings.Repeat("l", 32), Time: "23:59", FacilityName: strings.Repeat("f", 32), FacilityPhone: "+256000000001"}

// SMS is gated/sms.yaml: the copy of every SMS. Status "placeholder" (and
// any PENDING marker) blocks production.
type SMS struct {
	Status    string          `yaml:"status"`
	Languages []string        `yaml:"languages"`
	Templates map[string]Text `yaml:"templates"`
}

var (
	smsPlaceholder = regexp.MustCompile(`\{([a-z_]+)\}`)
	digitRun       = regexp.MustCompile(`[0-9][0-9 -]{6,}[0-9]`)
)

// smsLimit is one GSM-7 SMS segment.
const smsLimit = 160

// Validate checks every template: present in every language, only its own
// placeholders, no hard-coded number, GSM-7, one segment with long values.
func (s SMS) Validate() error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if s.Status != "placeholder" && s.Status != "approved" {
		bad("status %q (want placeholder or approved)", s.Status)
	}
	if !slices.Contains(s.Languages, "en") {
		bad("languages must include en (the fallback)")
	}
	for name := range s.Templates {
		if _, ok := smsPlaceholders[name]; !ok {
			bad("unknown template %s", name)
		}
	}
	for _, name := range slices.Sorted(mapKeys(smsPlaceholders)) {
		tx, ok := s.Templates[name]
		if !ok {
			bad("template %s is missing", name)
			continue
		}
		for _, lang := range s.Languages {
			text := tx[lang]
			if strings.TrimSpace(text) == "" {
				bad("%s/%s is missing", name, lang)
				continue
			}
			for _, m := range smsPlaceholder.FindAllStringSubmatch(text, -1) {
				if !slices.Contains(smsPlaceholders[name], m[1]) {
					bad("%s/%s: {%s} is not allowed in this message", name, lang, m[1])
				}
			}
			if digitRun.MatchString(text) {
				bad("%s/%s: a number is written into the text", name, lang)
			}
			out := s.render(name, text, worstCaseSMS)
			if !IsGSM7(out) {
				bad("%s/%s: leaves the GSM 03.38 basic character set", name, lang)
			}
			if len(out) > smsLimit {
				bad("%s/%s: %d characters with long values (limit %d)", name, lang, len(out), smsLimit)
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("sms: " + strings.Join(problems, "; "))
	}
	return nil
}

func (s SMS) render(name, text string, v SMSValues) string {
	return smsPlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		key := m[1 : len(m)-1]
		if !slices.Contains(smsPlaceholders[name], key) {
			return ""
		}
		return v.lookup(key)
	})
}

// Render fills a template in lang (English if lang has none). Only the
// template's own placeholders are filled.
func (s SMS) Render(name, lang string, v SMSValues) (string, error) {
	tx, ok := s.Templates[name]
	if _, known := smsPlaceholders[name]; !ok || !known {
		return "", fmt.Errorf("sms: unknown template %q", name)
	}
	text := tx[lang]
	if text == "" {
		text = tx["en"]
	}
	return s.render(name, text, v), nil
}

// texts returns every template as name/lang/text triples.
func (s SMS) texts() [][3]string {
	var out [][3]string
	for _, name := range slices.Sorted(mapKeys(s.Templates)) {
		for _, lang := range slices.Sorted(mapKeys(s.Templates[name])) {
			out = append(out, [3]string{"sms." + name, lang, s.Templates[name][lang]})
		}
	}
	return out
}
