package main

import (
	"fmt"

	"go.yaml.in/yaml/v4"

	"sautihealth.com/sauti/internal/location"
)

// mapView is the gazetteer as the UI's map draws it: each ward's centre and
// each landmark's point, named in English. Reference data, no personal data.
type mapView struct {
	Wards     []mapPlace `json:"wards"`
	Landmarks []mapPlace `json:"landmarks"`
}

type mapPlace struct {
	ID   string  `json:"id"`
	Ward string  `json:"ward,omitempty"` // a landmark's ward
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
	v := mapView{Wards: []mapPlace{}, Landmarks: []mapPlace{}}
	for _, w := range gaz.Wards {
		c := centre[w.ID]
		v.Wards = append(v.Wards, mapPlace{ID: w.ID, Name: w.Name.EN, Lat: c.Lat, Lon: c.Lon})
	}
	for _, l := range gaz.Landmarks {
		v.Landmarks = append(v.Landmarks, mapPlace{ID: l.ID, Ward: l.WardID, Name: l.Name.EN, Lat: l.Point.Lat, Lon: l.Point.Lon})
	}
	return v, nil
}
