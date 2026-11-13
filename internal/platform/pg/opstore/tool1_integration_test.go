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
	"sautihealth.com/sauti/internal/platform/pg/opstore"
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
	appendEv(t, s, id, incident.EvClassified, `{"severity":"yellow"}`, "tool1-test+sha256:000000000000")
	appendEv(t, s, id, incident.EvSessionResumed, `{"channel":"ivr"}`, "")
	appendEv(t, s, id, incident.EvScreenAnswered, `{"screen":6,"field":"ward","value":"w01"}`, "")
	appendEv(t, s, id, incident.EvScreenAnswered, fmt.Sprintf(`{"screen":7,"field":"landmark","value":"other","identifier_id":%q}`, ident), "")
	// The same answer again from another session is accepted (a changed one
	// is refused: TestSQLRefusesAChangedAnswer); last_screen stays 7.
	if _, err := s.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvScreenAnswered,
		Payload: json.RawMessage(`{"screen":3,"field":"category","value":"poisoning"}`), ActorKind: incident.ActorCaller,
		IdemKey: "ussd:at:other:s3"}); err != nil {
		t.Fatal(err)
	}
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
	if got.Language != "lg" || got.Category != "poisoning" || got.Landmark != "other" || got.LastScreen != 8 ||
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
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test+sha256:000000000000")
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

// CodeRabbit PR #6: the database refuses a screen answer that changes an
// answered field, as incident.Apply does (TestApplyRefusesAChangedAnswer),
// whatever later migrations make of the write path: the rule is a trigger on
// the event log. The same answer again is accepted, and the projection still
// equals the fold of its events.
func TestSQLRefusesAChangedAnswer(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.IncidentID
	for _, a := range []string{
		`{"screen":2,"field":"danger","value":"no"}`,
		`{"screen":3,"field":"category","value":"poisoning"}`,
		`{"screen":4,"field":"age_band","value":"age_18_59"}`,
		`{"screen":5,"field":"specific_danger","value":"no"}`,
	} {
		appendEv(t, s, id, incident.EvScreenAnswered, a, "")
	}
	appendEv(t, s, id, incident.EvClassified, `{"severity":"yellow"}`, testLogic)
	answer := func(payload, idem string) error {
		_, err := s.Append(ctx, incident.AppendParams{IncidentID: id, Type: incident.EvScreenAnswered,
			Payload: json.RawMessage(payload), ActorKind: incident.ActorCaller, IdemKey: idem})
		return err
	}
	for i, p := range []string{
		`{"screen":3,"field":"category","value":"trauma"}`,
		`{"screen":5,"field":"specific_danger","value":"yes"}`,
		`{"screen":2,"field":"danger","value":"yes"}`,
	} {
		if err := answer(p, fmt.Sprintf("ussd:at:old:%d", i)); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("changed answer %s: err = %v, want ErrIllegalTransition", p, err)
		}
	}
	if err := answer(`{"screen":3,"field":"category","value":"poisoning"}`, "ussd:at:old:same"); err != nil {
		t.Fatalf("same answer again: %v", err)
	}
	got, err := s.Projection(ctx, id)
	if err != nil || got.Category != "poisoning" || got.SpecificDanger != "no" || got.Danger != "no" || got.Version != 7 {
		t.Fatalf("projection = %+v, %v", got, err)
	}
	checked, drift, err := opstore.VerifyProjections(ctx, storeAs(t, c, "op_readonly"))
	if err != nil || checked != 1 || len(drift) != 0 {
		t.Fatalf("verify-projections: checked=%d drift=%v err=%v", checked, drift, err)
	}
}
