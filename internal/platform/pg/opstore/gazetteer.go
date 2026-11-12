package opstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
)

var (
	// ErrNoGazetteer reports that no gazetteer version has been loaded.
	ErrNoGazetteer = errors.New("opstore: no gazetteer loaded")
	// ErrGazetteerVersionExists reports different content under a version
	// that is already loaded: versions are immutable.
	ErrGazetteerVersionExists = errors.New("opstore: gazetteer version already loaded with different content")
)

// Beginner starts a transaction (a *pgx.Conn or a *pgxpool.Pool).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// LoadGazetteer stores one gazetteer version and makes it the version in
// force, in one transaction, as the store owner. Loading the same file again
// is a no-op (loaded = false); different content under an existing version is
// ErrGazetteerVersionExists. raw is the file exactly as read, for its hash.
func LoadGazetteer(ctx context.Context, owner Beginner, f location.File, raw []byte) (loaded bool, err error) {
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	tx, err := owner.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var existing string
	switch err := tx.QueryRow(ctx, "SELECT sha256 FROM op.gazetteer_versions WHERE version = $1", f.Version).Scan(&existing); {
	case err == nil && existing == digest:
		return false, nil
	case err == nil:
		return false, fmt.Errorf("%w: %s", ErrGazetteerVersionExists, f.Version)
	case !errors.Is(err, pgx.ErrNoRows):
		return false, err
	}
	batch := &pgx.Batch{}
	batch.Queue("INSERT INTO op.gazetteer_versions (version, sha256, status) VALUES ($1, $2, $3)", f.Version, digest, f.Status)
	for i, w := range f.Wards {
		batch.Queue(`INSERT INTO op.wards (version, id, menu_order, name_en, name_lg, centre_col, centre_row)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, f.Version, w.ID, i+1, w.Name.EN, w.Name.LG, w.Centre.Col, w.Centre.Row)
		for _, z := range w.Zones {
			batch.Queue("INSERT INTO op.ward_zones (version, ward_id, zone_col, zone_row) VALUES ($1, $2, $3, $4)",
				f.Version, w.ID, z.Col, z.Row)
		}
	}
	for i, l := range f.Landmarks {
		batch.Queue(`INSERT INTO op.landmarks (version, id, list_order, ward_id, name_en, name_lg, lat, lon, zone_col, zone_row, menu)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			f.Version, l.ID, i+1, l.WardID, l.Name.EN, l.Name.LG, l.Point.Lat, l.Point.Lon, l.Cell.Col, l.Cell.Row, l.Menu)
		for j, a := range l.Aliases {
			batch.Queue("INSERT INTO op.landmark_aliases (version, landmark_id, alias_order, ward_id, alias_norm) VALUES ($1, $2, $3, $4, $5)",
				f.Version, l.ID, j+1, l.WardID, a)
		}
	}
	for i, st := range f.Stages {
		batch.Queue(`INSERT INTO op.stages (version, id, list_order, ward_id, name_en, name_lg, lat, lon, zone_col, zone_row)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			f.Version, st.ID, i+1, st.WardID, st.Name.EN, st.Name.LG, st.Point.Lat, st.Point.Lon, st.Cell.Col, st.Cell.Row)
	}
	for i, fc := range f.Facilities {
		batch.Queue(`INSERT INTO op.facilities (version, id, list_order, name_en, name_lg, lat, lon, zone_col, zone_row, phone, forwarding_enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			f.Version, fc.ID, i+1, fc.Name.EN, fc.Name.LG, fc.Point.Lat, fc.Point.Lon, fc.Cell.Col, fc.Cell.Row, fc.Phone, fc.ForwardingEnabled)
	}
	batch.Queue("INSERT INTO op.config_versions (kind, version, fingerprint) VALUES ('gazetteer', $1, $2)", f.Version, "sha256:"+digest)
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return false, fmt.Errorf("opstore: load gazetteer %s: %w", f.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Snapshot returns the gazetteer version in force (location.Gazetteer).
func (s *Store) Snapshot(ctx context.Context) (location.File, error) {
	cur, err := s.q.CurrentGazetteer(ctx)
	if err != nil {
		return location.File{}, err
	}
	if len(cur) == 0 {
		return location.File{}, ErrNoGazetteer
	}
	v := cur[0].Version
	f := location.File{Version: v, Status: cur[0].Status}
	wards, err := s.q.ListWards(ctx, v)
	if err != nil {
		return location.File{}, err
	}
	idx := map[string]int{}
	for i, w := range wards {
		idx[w.ID] = i
		f.Wards = append(f.Wards, location.Ward{ID: w.ID, Name: location.Names{EN: w.NameEn, LG: w.NameLg},
			Centre: location.Cell{Col: int(w.CentreCol), Row: int(w.CentreRow)}})
	}
	zones, err := s.q.ListWardZones(ctx, v)
	if err != nil {
		return location.File{}, err
	}
	for _, z := range zones {
		w := &f.Wards[idx[z.WardID]]
		w.Zones = append(w.Zones, location.Cell{Col: int(z.ZoneCol), Row: int(z.ZoneRow)})
	}
	lms, err := s.q.ListLandmarks(ctx, v)
	if err != nil {
		return location.File{}, err
	}
	lidx := map[string]int{}
	for i, l := range lms {
		lidx[l.ID] = i
		f.Landmarks = append(f.Landmarks, location.Landmark{ID: l.ID, WardID: l.WardID,
			Name: location.Names{EN: l.NameEn, LG: l.NameLg}, Point: location.Point{Lat: l.Lat, Lon: l.Lon},
			Cell: location.Cell{Col: int(l.ZoneCol), Row: int(l.ZoneRow)}, Menu: int(l.Menu)})
	}
	aliases, err := s.q.ListAliases(ctx, v)
	if err != nil {
		return location.File{}, err
	}
	for _, a := range aliases {
		l := &f.Landmarks[lidx[a.LandmarkID]]
		l.Aliases = append(l.Aliases, a.AliasNorm)
	}
	stages, err := s.q.ListStages(ctx, v)
	if err != nil {
		return location.File{}, err
	}
	for _, st := range stages {
		f.Stages = append(f.Stages, location.Stage{ID: st.ID, WardID: st.WardID, Name: location.Names{EN: st.NameEn, LG: st.NameLg},
			Point: location.Point{Lat: st.Lat, Lon: st.Lon}, Cell: location.Cell{Col: int(st.ZoneCol), Row: int(st.ZoneRow)}})
	}
	facilities, err := s.q.ListFacilities(ctx, v)
	if err != nil {
		return location.File{}, err
	}
	for _, fc := range facilities {
		f.Facilities = append(f.Facilities, location.Facility{ID: fc.ID, Name: location.Names{EN: fc.NameEn, LG: fc.NameLg},
			Point: location.Point{Lat: fc.Lat, Lon: fc.Lon}, Cell: location.Cell{Col: int(fc.ZoneCol), Row: int(fc.ZoneRow)},
			Phone: fc.Phone, ForwardingEnabled: fc.ForwardingEnabled})
	}
	return f, nil
}

// Match returns the best alias match within one ward (location.Gazetteer).
func (s *Store) Match(ctx context.Context, version, wardID, normalised string) (location.Match, error) {
	rows, err := s.q.MatchLandmark(ctx, opdb.MatchLandmarkParams{Version: version, WardID: wardID, Text: normalised})
	if err != nil {
		return location.Match{}, err
	}
	if len(rows) == 0 {
		return location.Match{}, nil
	}
	return location.Match{LandmarkID: rows[0].LandmarkID, Similarity: float64(rows[0].Similarity), Found: true}, nil
}
