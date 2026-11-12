//go:build integration

package opstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// The SQL write path projects Tool 1 answers and the dispatch location
// exactly as incident.Apply does (spec §6.2).
func TestProjectionParityTool1(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.IncidentID
	ident := uuid.NewV7()
	for _, a := range []string{
		`{"screen":1,"field":"language","value":"lg"}`,
		`{"screen":2,"field":"danger","value":"unknown"}`,
		`{"screen":3,"field":"category","value":"poisoning"}`,
		`{"screen":4,"field":"age_band","value":"age_18_59"}`,
		`{"screen":5,"field":"specific_danger","value":"no"}`,
	} {
		appendEv(t, s, id, incident.EvScreenAnswered, a, "")
	}
	appendEv(t, s, id, incident.EvClassified, `{"severity":"yellow"}`, "tool1-test+sha256:00")
	appendEv(t, s, id, incident.EvSessionResumed, `{"channel":"ivr"}`, "")
	appendEv(t, s, id, incident.EvScreenAnswered, `{"screen":6,"field":"ward","value":"w01"}`, "")
	appendEv(t, s, id, incident.EvScreenAnswered, fmt.Sprintf(`{"screen":7,"field":"landmark","value":"other","identifier_id":%q}`, ident), "")
	appendEv(t, s, id, incident.EvScreenAnswered, `{"screen":3,"field":"category","value":"trauma"}`, "")
	appendEv(t, s, id, incident.EvDispatchReady, `{"zone":{"col":901,"row":-70},"location_quality":"fuzzy"}`, "")
	appendEv(t, s, id, incident.EvScreenAnswered, `{"screen":8,"field":"callback","value":"same_number"}`, "")

	evs, err := s.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want, err := incident.Fold(evs)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got, err := s.Projection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d := incident.Diff(got, want); d != "" {
		t.Fatalf("DB projection differs from Fold: %s", d)
	}
	if got.Language != "lg" || got.Category != "trauma" || got.Landmark != "other" || got.LastScreen != 8 ||
		got.Zone == nil || *got.Zone != (incident.Zone{Col: 901, Row: -70}) || got.LocationQuality != "fuzzy" ||
		got.State != incident.StateDispatching {
		t.Fatalf("projection = %+v", got)
	}
}

// Whatever incident.Apply refuses, the database refuses too, so a stored
// history always folds and no identifier reaches a payload through SQL.
func TestSQLRejectsWhatFoldRejects(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[incident.EventType][]string{
		incident.EvScreenAnswered: {
			`{}`,
			`{"screen":2,"answer":"1"}`,
			`{"screen":9,"field":"danger","value":"yes"}`,
			`{"screen":0,"field":"language","value":"en"}`,
			`{"screen":2.5,"field":"danger","value":"yes"}`,
			`{"screen":"2","field":"danger","value":"yes"}`,
			`{"screen":3,"field":"danger","value":"yes"}`,
			`{"screen":2,"field":"danger","value":""}`,
			`{"screen":2,"field":"colour","value":"red"}`,
			`{"screen":8,"field":"callback","value":"0772123456"}`,
			`{"screen":7,"field":"landmark","value":"Kawempe market"}`,
			`{"screen":2,"field":"danger","value":"yes","phone":"0772123456"}`,
			`{"screen":8,"field":"callback","value":"provided"}`,
			`{"screen":7,"field":"landmark","value":"other","identifier_id":"not-a-uuid"}`,
			`{"screen":2,"field":"danger","value":"yes","identifier_id":"0195f3a2-7c1e-7d4b-9a10-2b3c4d5e6f70"}`,
		},
		incident.EvSessionResumed: {`{}`, `{"channel":"sms"}`, `{"channel":"ivr","phone":"0772123456"}`},
	}
	for typ, payloads := range cases {
		for i, p := range payloads {
			_, err := s.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: typ, Payload: json.RawMessage(p),
				ActorKind: incident.ActorSystem, IdemKey: fmt.Sprintf("bad:%s:%d", typ, i)})
			if !errors.Is(err, incident.ErrIllegalTransition) {
				t.Errorf("%s %s: err = %v, want ErrIllegalTransition", typ, p, err)
			}
		}
	}
	// dispatch_ready validation, from a triaged incident.
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test")
	for i, p := range []string{
		`{"zone":{"col":1,"row":2},"location_quality":"guess"}`,
		`{"zone":{"col":1},"location_quality":"ward"}`,
		`{"location_quality":"ward"}`,
		`{"zone":{"col":1.5,"row":2},"location_quality":"ward"}`,
		`{"zone":{"col":1,"row":2,"lat":0.3},"location_quality":"ward"}`,
	} {
		_, err := s.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: incident.EvDispatchReady,
			Payload: json.RawMessage(p), ActorKind: incident.ActorSystem, IdemKey: fmt.Sprintf("bad:dr:%d", i)})
		if !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("dispatch_ready %s: err = %v, want ErrIllegalTransition", p, err)
		}
	}
}
