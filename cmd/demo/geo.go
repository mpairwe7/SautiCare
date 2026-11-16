package main

import (
	"fmt"

	"go.yaml.in/yaml/v4"

	"sautihealth.com/sauti/internal/location"
)

// mapView is the gazetteer as the UI's map draws it: each ward's centre, each
// landmark's, stage's and facility's point, named in English. A responder's
// stage stands in for a smartphone's location in the demo. Reference data, no
// personal data.
type mapView struct {
	Wards      []mapPlace `json:"wards"`
	Landmarks  []mapPlace `json:"landmarks"`
	Stages     []mapPlace `json:"stages"`
	Facilities []mapPlace `json:"facilities"`
}

type mapPlace struct {
	ID   string  `json:"id"`
	Ward string  `json:"ward,omitempty"` // a landmark's or a stage's ward
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

// newMapView reads the ward centres from the gazetteer file itself, which
// location.ParseFile has already validated: location.Ward keeps only the
// centre's grid cell, as the operational store does.
func newMapView(raw []byte, gaz location.File) (mapView, error) {
	var file struct {
		Wards []struct {
			ID     string         `yaml:"id"`
			Centre location.Point `yaml:"centre"`
		} `yaml:"wards"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return mapView{}, fmt.Errorf("demo: gazetteer ward centres: %w", err)
	}
	centre := map[string]location.Point{}
	for _, w := range file.Wards {
		centre[w.ID] = w.Centre
	}
	v := mapView{Wards: []mapPlace{}, Landmarks: []mapPlace{}, Stages: []mapPlace{}, Facilities: []mapPlace{}}
	for _, w := range gaz.Wards {
		c := centre[w.ID]
		v.Wards = append(v.Wards, mapPlace{ID: w.ID, Name: w.Name.EN, Lat: c.Lat, Lon: c.Lon})
	}
	for _, l := range gaz.Landmarks {
		v.Landmarks = append(v.Landmarks, mapPlace{ID: l.ID, Ward: l.WardID, Name: l.Name.EN, Lat: l.Point.Lat, Lon: l.Point.Lon})
	}
	for _, sg := range gaz.Stages {
		v.Stages = append(v.Stages, mapPlace{ID: sg.ID, Ward: sg.WardID, Name: sg.Name.EN, Lat: sg.Point.Lat, Lon: sg.Point.Lon})
	}
	for _, f := range gaz.Facilities {
		v.Facilities = append(v.Facilities, mapPlace{ID: f.ID, Name: f.Name.EN, Lat: f.Point.Lat, Lon: f.Point.Lon})
	}
	return v, nil
}
