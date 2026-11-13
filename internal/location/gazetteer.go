package location

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Reserved answer values: a ward answer of "elsewhere" is the out-of-area
// option, a landmark answer of "other" is free text (or, on IVR, "another
// landmark"). No gazetteer ID may take either.
const (
	WardElsewhere = "elsewhere"
	LandmarkOther = "other"
	// MaxMenuLandmarks is the per-ward landmark menu size; option 6 is
	// free text (Build Prompt §10.1).
	MaxMenuLandmarks = 5
	// MaxWards is the ward menu size; "Somewhere else" follows as option 9,
	// the last single digit.
	MaxWards = 8
)

// Gazetteer statuses. A placeholder gazetteer never runs in production.
const (
	StatusPlaceholder = "placeholder"
	StatusApproved    = "approved"
)

// IDs become screen.answered values, so they follow the same code pattern.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// Names holds a place name in each caller language.
type Names struct {
	EN string `yaml:"en"`
	LG string `yaml:"lg"`
}

// Point is a WGS84 position.
type Point struct {
	Lat float64 `yaml:"lat"`
	Lon float64 `yaml:"lon"`
}

// Ward is one ward on the screen 6 menu. Zones are the cells dispatch uses
// when only the ward is known: its centre, any listed zones and the cells of
// its landmarks, sorted.
type Ward struct {
	ID     string
	Name   Names
	Centre Cell
	Zones  []Cell
}

// Landmark is one gazetteer landmark. Menu is its position (1-5) on the ward's
// screen 7 menu, or 0 if it is only reachable by free-text matching. Aliases
// are normalised and include both names.
type Landmark struct {
	ID      string
	WardID  string
	Name    Names
	Point   Point
	Cell    Cell
	Menu    int
	Aliases []string
}

// File is one version of the gazetteer (config/gated/gazetteer.yaml). It is
// reference data with no personal data, agreed with ward leadership, versioned
// and frozen during the study window (Build Prompt §9, §12).
type File struct {
	Version    string
	Status     string
	Wards      []Ward
	Landmarks  []Landmark
	Stages     []Stage
	Facilities []Facility
}

// Stage is a boda-boda stage where transporters wait; a responder's stage
// cell is where ranking measures zone distance from (Build Prompt §10.4).
type Stage struct {
	ID     string
	WardID string
	Name   Names
	Point  Point
	Cell   Cell
}

// Facility is a registered health facility. ForwardingEnabled facilities
// receive forwarded requests at 2.5 km (Build Prompt §10.4); Phone is the
// facility's public number (an institution's, not a person's).
type Facility struct {
	ID                string
	Name              Names
	Point             Point
	Cell              Cell
	Phone             string
	ForwardingEnabled bool
}

var facilityPhone = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Match is the best free-text match within a ward.
type Match struct {
	LandmarkID string
	Similarity float64
	Found      bool
}

// Gazetteer is the location context's port to the stored gazetteer.
type Gazetteer interface {
	// Snapshot returns the version in force.
	Snapshot(ctx context.Context) (File, error)
	// Match returns the landmark in wardID whose alias best matches the
	// normalised text, by trigram similarity.
	Match(ctx context.Context, version, wardID, normalised string) (Match, error)
}

type rawFile struct {
	Version string `yaml:"version"`
	Status  string `yaml:"status"`
	Wards   []struct {
		ID     string `yaml:"id"`
		Name   Names  `yaml:"name"`
		Centre Point  `yaml:"centre"`
		Zones  []Cell `yaml:"zones"`
	} `yaml:"wards"`
	Landmarks []struct {
		ID      string   `yaml:"id"`
		Ward    string   `yaml:"ward"`
		Name    Names    `yaml:"name"`
		Point   Point    `yaml:"point"`
		Menu    int      `yaml:"menu"`
		Aliases []string `yaml:"aliases"`
	} `yaml:"landmarks"`
	Stages []struct {
		ID    string `yaml:"id"`
		Ward  string `yaml:"ward"`
		Name  Names  `yaml:"name"`
		Point Point  `yaml:"point"`
	} `yaml:"stages"`
	Facilities []struct {
		ID                string `yaml:"id"`
		Name              Names  `yaml:"name"`
		Point             Point  `yaml:"point"`
		Phone             string `yaml:"phone"`
		ForwardingEnabled bool   `yaml:"forwarding_enabled"`
	} `yaml:"facilities"`
}

func validID(id string) error {
	if !idPattern.MatchString(id) || id == WardElsewhere || id == LandmarkOther {
		return fmt.Errorf("location: id %q must be a code (lower-case letter first) and not reserved", id)
	}
	return nil
}

func validNames(id string, n Names) error {
	if strings.TrimSpace(n.EN) == "" || strings.TrimSpace(n.LG) == "" {
		return fmt.Errorf("location: %s needs a name in en and lg", id)
	}
	return nil
}

// ParseFile reads and validates a gazetteer file.
func ParseFile(data []byte) (File, error) {
	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return File{}, errors.New("location: empty gazetteer")
		}
		return File{}, fmt.Errorf("location: gazetteer: %w", err)
	}
	if strings.TrimSpace(raw.Version) == "" {
		return File{}, errors.New("location: gazetteer needs a version")
	}
	if raw.Status != StatusPlaceholder && raw.Status != StatusApproved {
		return File{}, fmt.Errorf("location: gazetteer status %q (want placeholder or approved)", raw.Status)
	}
	if len(raw.Wards) == 0 || len(raw.Wards) > MaxWards {
		return File{}, fmt.Errorf("location: gazetteer has %d wards (want 1-%d: the ward menu and Somewhere else are single digits)", len(raw.Wards), MaxWards)
	}
	f := File{Version: raw.Version, Status: raw.Status}
	wardIdx := map[string]int{}
	seen := map[string]bool{}
	for _, w := range raw.Wards {
		if err := validID(w.ID); err != nil {
			return File{}, err
		}
		if seen[w.ID] {
			return File{}, fmt.Errorf("location: duplicate id %s", w.ID)
		}
		seen[w.ID] = true
		if err := validNames(w.ID, w.Name); err != nil {
			return File{}, err
		}
		centre, err := CellAt(w.Centre.Lat, w.Centre.Lon)
		if err != nil {
			return File{}, fmt.Errorf("location: ward %s centre: %w", w.ID, err)
		}
		wardIdx[w.ID] = len(f.Wards)
		f.Wards = append(f.Wards, Ward{ID: w.ID, Name: w.Name, Centre: centre, Zones: append([]Cell{centre}, w.Zones...)})
	}
	menuSlots := map[string]map[int]bool{}
	for _, l := range raw.Landmarks {
		if err := validID(l.ID); err != nil {
			return File{}, err
		}
		if seen[l.ID] {
			return File{}, fmt.Errorf("location: duplicate id %s", l.ID)
		}
		seen[l.ID] = true
		wi, ok := wardIdx[l.Ward]
		if !ok {
			return File{}, fmt.Errorf("location: landmark %s names unknown ward %q", l.ID, l.Ward)
		}
		if err := validNames(l.ID, l.Name); err != nil {
			return File{}, err
		}
		cell, err := CellAt(l.Point.Lat, l.Point.Lon)
		if err != nil {
			return File{}, fmt.Errorf("location: landmark %s: %w", l.ID, err)
		}
		if l.Menu < 0 || l.Menu > MaxMenuLandmarks {
			return File{}, fmt.Errorf("location: landmark %s menu position %d (want 1-%d or none)", l.ID, l.Menu, MaxMenuLandmarks)
		}
		if l.Menu > 0 {
			if menuSlots[l.Ward] == nil {
				menuSlots[l.Ward] = map[int]bool{}
			}
			if menuSlots[l.Ward][l.Menu] {
				return File{}, fmt.Errorf("location: ward %s menu position %d used twice", l.Ward, l.Menu)
			}
			menuSlots[l.Ward][l.Menu] = true
		}
		var aliases []string
		for _, a := range append([]string{l.Name.EN, l.Name.LG}, l.Aliases...) {
			if n := Normalise(a); n != "" && !slices.Contains(aliases, n) {
				aliases = append(aliases, n)
			}
		}
		if len(aliases) == 0 {
			return File{}, fmt.Errorf("location: landmark %s has no matchable name", l.ID)
		}
		f.Landmarks = append(f.Landmarks, Landmark{ID: l.ID, WardID: l.Ward, Name: l.Name, Point: l.Point,
			Cell: cell, Menu: l.Menu, Aliases: aliases})
		f.Wards[wi].Zones = append(f.Wards[wi].Zones, cell)
	}
	for _, st := range raw.Stages {
		if err := validID(st.ID); err != nil {
			return File{}, err
		}
		if seen[st.ID] {
			return File{}, fmt.Errorf("location: duplicate id %s", st.ID)
		}
		seen[st.ID] = true
		if _, ok := wardIdx[st.Ward]; !ok {
			return File{}, fmt.Errorf("location: stage %s names unknown ward %q", st.ID, st.Ward)
		}
		if err := validNames(st.ID, st.Name); err != nil {
			return File{}, err
		}
		cell, err := CellAt(st.Point.Lat, st.Point.Lon)
		if err != nil {
			return File{}, fmt.Errorf("location: stage %s: %w", st.ID, err)
		}
		f.Stages = append(f.Stages, Stage{ID: st.ID, WardID: st.Ward, Name: st.Name, Point: st.Point, Cell: cell})
	}
	for _, fc := range raw.Facilities {
		if err := validID(fc.ID); err != nil {
			return File{}, err
		}
		if seen[fc.ID] {
			return File{}, fmt.Errorf("location: duplicate id %s", fc.ID)
		}
		seen[fc.ID] = true
		if err := validNames(fc.ID, fc.Name); err != nil {
			return File{}, err
		}
		if fc.Phone != "" && !facilityPhone.MatchString(fc.Phone) {
			return File{}, fmt.Errorf("location: facility %s phone must be E.164", fc.ID)
		}
		if fc.ForwardingEnabled && fc.Phone == "" {
			return File{}, fmt.Errorf("location: facility %s forwards but has no phone", fc.ID)
		}
		cell, err := CellAt(fc.Point.Lat, fc.Point.Lon)
		if err != nil {
			return File{}, fmt.Errorf("location: facility %s: %w", fc.ID, err)
		}
		f.Facilities = append(f.Facilities, Facility{ID: fc.ID, Name: fc.Name, Point: fc.Point, Cell: cell,
			Phone: fc.Phone, ForwardingEnabled: fc.ForwardingEnabled})
	}
	for i := range f.Wards {
		z := f.Wards[i].Zones
		slices.SortFunc(z, func(a, b Cell) int {
			if a.Row != b.Row {
				return a.Row - b.Row
			}
			return a.Col - b.Col
		})
		f.Wards[i].Zones = slices.Compact(z)
	}
	return f, nil
}

// Ward returns the ward with the given ID.
func (f File) Ward(id string) (Ward, bool) {
	for _, w := range f.Wards {
		if w.ID == id {
			return w, true
		}
	}
	return Ward{}, false
}

// Landmark returns the landmark with the given ID.
func (f File) Landmark(id string) (Landmark, bool) {
	for _, l := range f.Landmarks {
		if l.ID == id {
			return l, true
		}
	}
	return Landmark{}, false
}

// Stage returns the stage with the given ID.
func (f File) Stage(id string) (Stage, bool) {
	for _, s := range f.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return Stage{}, false
}

// Menu returns a ward's screen 7 menu landmarks in menu order.
func (f File) Menu(wardID string) []Landmark {
	var out []Landmark
	for _, l := range f.Landmarks {
		if l.WardID == wardID && l.Menu > 0 {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b Landmark) int { return a.Menu - b.Menu })
	return out
}

// ErrNotProductionReady reports a gazetteer that must never run live.
var ErrNotProductionReady = errors.New("location: gazetteer not production ready")

// ProductionReady reports whether f may run with SAUTI_ENV=production: the
// ward and landmark lists must be the ones agreed with ward leadership and
// collected by the field team, not placeholders.
func ProductionReady(f File) error {
	if f.Status != StatusApproved {
		return fmt.Errorf("%w: status %s", ErrNotProductionReady, f.Status)
	}
	return nil
}
