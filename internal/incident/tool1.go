package incident

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"uuid"
)

// Tool 1 field names, used in screen.answered payloads. The eight screens
// and their order are fixed by Build Prompt §10.1 and Engineering Guide §4;
// the wording and option lists are protocol-authored configuration.
const (
	FieldLanguage       = "language"
	FieldDanger         = "danger"
	FieldCategory       = "category"
	FieldAgeBand        = "age_band"
	FieldSpecificDanger = "specific_danger"
	FieldWard           = "ward"
	FieldLandmark       = "landmark"
	FieldCallback       = "callback"
)

// Zone is a 500 m grid cell (UTM 36N), as recorded on dispatch_ready.
type Zone struct{ Col, Row int }

// fieldScreens fixes which screen carries which field.
var fieldScreens = map[string]int{
	FieldLanguage: 1, FieldDanger: 2, FieldCategory: 3, FieldAgeBand: 4,
	FieldSpecificDanger: 5, FieldWard: 6, FieldLandmark: 7, FieldCallback: 8,
}

// Coded values start with a lower-case letter, so a phone number, a digit
// string or free text can never be a value. The SQL write path applies the
// same patterns (db/operational/migrations/0006_tool1_projection.sql).
var (
	codeValue    = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	screenNumber = regexp.MustCompile(`^[1-8]$`)
	cellIndex    = regexp.MustCompile(`^(0|-?[1-9][0-9]{0,6})$`)
	uuidText     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Location qualities recorded on dispatch_ready: a listed landmark, a
// free-text landmark matched to the gazetteer, or only the ward.
const (
	QualityLandmark = "landmark"
	QualityFuzzy    = "fuzzy"
	QualityWard     = "ward"
)

// Callback answers: the number the session came from, or another number
// sealed in op.incident_identifiers and referenced by identifier_id.
const (
	CallbackSameNumber = "same_number"
	CallbackProvided   = "provided"
	LandmarkOther      = "other"
)

// ScreenAnswer is a validated screen.answered payload.
type ScreenAnswer struct {
	Screen       int        `json:"screen"`
	Field        string     `json:"field"`
	Value        string     `json:"value"`
	IdentifierID *uuid.UUID `json:"identifier_id,omitempty"`
}

// Payload encodes the answer for an event.
func (a ScreenAnswer) Payload() (json.RawMessage, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	if _, err := ParseScreenAnswer(b); err != nil {
		return nil, err
	}
	return b, nil
}

func strictKeys(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k := range m {
		if !slices.Contains(allowed, k) {
			return nil, fmt.Errorf("unexpected key %q", k)
		}
	}
	return m, nil
}

func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	return s, raw != nil && json.Unmarshal(raw, &s) == nil
}

// jsonNumber reads a JSON number (never a quoted one, which json.Number
// would otherwise accept) whose text matches pattern.
func jsonNumber(raw json.RawMessage, pattern *regexp.Regexp) (int, bool) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] == '"' {
		return 0, false
	}
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if raw == nil || d.Decode(&n) != nil || !pattern.MatchString(n.String()) {
		return 0, false
	}
	v, err := strconv.Atoi(n.String())
	return v, err == nil
}

// ParseScreenAnswer validates a screen.answered payload: exactly the keys
// screen, field, value and (where allowed) identifier_id; the field matches
// its screen; the value is a code. identifier_id is required for a provided
// callback number and allowed for a free-text landmark, nowhere else.
func ParseScreenAnswer(raw json.RawMessage) (ScreenAnswer, error) {
	bad := func(why string) (ScreenAnswer, error) {
		return ScreenAnswer{}, fmt.Errorf("%w: screen.answered: %s", ErrIllegalTransition, why)
	}
	m, err := strictKeys(raw, "screen", "field", "value", "identifier_id")
	if err != nil {
		return bad(err.Error())
	}
	var a ScreenAnswer
	var ok bool
	if a.Screen, ok = jsonNumber(m["screen"], screenNumber); !ok {
		return bad("screen must be 1-8")
	}
	if a.Field, ok = jsonString(m["field"]); !ok || fieldScreens[a.Field] != a.Screen {
		return bad("field does not match screen")
	}
	if a.Value, ok = jsonString(m["value"]); !ok || !codeValue.MatchString(a.Value) {
		return bad("value must be a code")
	}
	if raw, present := m["identifier_id"]; present {
		s, ok := jsonString(raw)
		id, err := uuid.Parse(s)
		if !ok || !uuidText.MatchString(s) || err != nil {
			return bad("identifier_id must be a UUID")
		}
		a.IdentifierID = &id
	}
	provided := a.Field == FieldCallback && a.Value == CallbackProvided
	otherLandmark := a.Field == FieldLandmark && a.Value == LandmarkOther
	switch {
	case provided && a.IdentifierID == nil:
		return bad("a provided callback number needs identifier_id")
	case a.IdentifierID != nil && !provided && !otherLandmark:
		return bad("identifier_id is allowed only for a provided callback or a free-text landmark")
	}
	return a, nil
}

// DispatchLocation is the optional location on a dispatch_ready payload.
type DispatchLocation struct {
	Zone    *Zone
	Quality string
}

// ParseDispatchLocation validates the zone and location quality of a
// dispatch_ready payload. Both are absent, or both are present and valid.
func ParseDispatchLocation(raw json.RawMessage) (DispatchLocation, error) {
	bad := func(why string) (DispatchLocation, error) {
		return DispatchLocation{}, fmt.Errorf("%w: dispatch_ready: %s", ErrIllegalTransition, why)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return bad(err.Error())
	}
	zraw, hasZone := m["zone"]
	qraw, hasQuality := m["location_quality"]
	if !hasZone && !hasQuality {
		return DispatchLocation{}, nil
	}
	q, ok := jsonString(qraw)
	if !hasZone || !ok || (q != QualityLandmark && q != QualityFuzzy && q != QualityWard) {
		return bad("zone and a known location_quality go together")
	}
	zm, err := strictKeys(zraw, "col", "row")
	if err != nil {
		return bad("zone: " + err.Error())
	}
	col, okc := jsonNumber(zm["col"], cellIndex)
	row, okr := jsonNumber(zm["row"], cellIndex)
	if !okc || !okr {
		return bad("zone col and row must be integers")
	}
	return DispatchLocation{Zone: &Zone{Col: col, Row: row}, Quality: q}, nil
}

func parseResumed(raw json.RawMessage) error {
	m, err := strictKeys(raw, "channel")
	if err == nil {
		if c, ok := jsonString(m["channel"]); ok && (c == string(ChannelUSSDAT) || c == string(ChannelUSSDUCC) || c == string(ChannelIVR)) {
			return nil
		}
	}
	return fmt.Errorf("%w: session.resumed needs a known channel", ErrIllegalTransition)
}

// set records an answer on the projection. An answered field never changes:
// a different value is refused, the same value again is accepted. The SQL
// write path applies the same rule (op.screen_answer_unchanged, migration
// 0006).
func (p *Projection) set(a ScreenAnswer) error {
	var f *string
	switch a.Field {
	case FieldLanguage:
		f = &p.Language
	case FieldDanger:
		f = &p.Danger
	case FieldCategory:
		f = &p.Category
	case FieldAgeBand:
		f = &p.AgeBand
	case FieldSpecificDanger:
		f = &p.SpecificDanger
	case FieldWard:
		f = &p.Ward
	case FieldLandmark:
		f = &p.Landmark
	}
	if f != nil {
		if *f != "" && *f != a.Value {
			return fmt.Errorf("%w: screen.answered: %s is already answered", ErrIllegalTransition, a.Field)
		}
		*f = a.Value
	}
	p.LastScreen = max(p.LastScreen, a.Screen)
	return nil
}
