//go:build integration

package opstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// C1: an answer and what it implies are one transaction. A batch whose last
// event the write path refuses stores none of its events; a good batch is
// applied in order (the classification's state change lets the dispatch that
// follows it in), and a retried batch writes nothing twice.
func TestAppendBatchIsAllOrNothing(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	id := o.IncidentID
	ev := func(typ incident.EventType, payload, key, logic string) incident.AppendParams {
		return incident.AppendParams{IncidentID: id, Type: typ, Payload: json.RawMessage(payload),
			ActorKind: incident.ActorSystem, IdemKey: key, LogicVersion: logic}
	}
	answer := ev(incident.EvScreenAnswered, `{"screen":5,"field":"specific_danger","value":"yes"}`, "s5", "")
	flag := ev(incident.EvFlagged, `{"flag":"triage_rule_error"}`, "rule_error", "")
	bad := ev(incident.EvClassified, `{"severity":"purple"}`, "classify", testLogic)
	if _, err := s.AppendBatch(ctx, []incident.AppendParams{answer, flag, bad}); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Fatalf("batch with a refused event: %v", err)
	}
	if evs, err := s.Events(ctx, id); err != nil || len(evs) != 1 {
		t.Fatalf("a refused batch left %d events (%v)", len(evs), err)
	}
	if p, _ := s.Projection(ctx, id); p.Version != 1 || p.SpecificDanger != "" || len(p.Flags) != 0 {
		t.Fatalf("a refused batch changed the projection: %+v", p)
	}

	good := []incident.AppendParams{answer, ev(incident.EvClassified, `{"severity":"red"}`, "classify", testLogic), flag,
		ev(incident.EvEscalated, `{"reason":"triage_rule_error"}`, "escalate", ""),
		ev(incident.EvDispatchReady, `{"zone":{"col":901,"row":-70},"location_quality":"landmark"}`, "dispatch_ready", "")}
	out, err := s.AppendBatch(ctx, good)
	if err != nil || len(out) != len(good) {
		t.Fatalf("good batch: %d results, %v", len(out), err)
	}
	if out[1].NewState != incident.StateTriaged || out[4].NewState != incident.StateDispatching {
		t.Fatalf("states %s, %s", out[1].NewState, out[4].NewState)
	}
	again, err := s.AppendBatch(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	for i, a := range again {
		if !a.Duplicate || a.Event.ID != out[i].Event.ID {
			t.Fatalf("retry %d: %+v", i, a)
		}
	}
	evs, _ := s.Events(ctx, id)
	p, _ := s.Projection(ctx, id)
	if len(evs) != 6 || p.Severity != incident.SevRed || !p.Escalated || p.T[2] == nil {
		t.Fatalf("%d events, projection %+v", len(evs), p)
	}
}
