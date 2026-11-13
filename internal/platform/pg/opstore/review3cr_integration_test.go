//go:build integration

package opstore_test

import (
	"errors"
	"testing"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// CodeRabbit PR #3 (review of ead62cf), finding 1: a flag is a JSON string.
// The write path reads the flag with ->>, which turns {"flag":123} into the
// non-empty text "123" and accepts it; incident.Apply decodes the flag into
// a Go string and rejects the stored event, so Fold could no longer replay
// the history. A CHECK constraint on op.incident_events (0027) rejects it
// below every version of the write path.
func TestFlagEventNeedsAJSONStringFlag(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := pgtest.Context(t)
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"flag":123}`, `{"flag":true}`, `{"flag":["test_call"]}`, `{"flag":{"test_call":1}}`} {
		if err := appendErr(s, o.IncidentID, incident.EvFlagged, bad, 0); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("flag event %s: err = %v, want ErrIllegalTransition", bad, err)
		}
	}
	appendEv(t, s, o.IncidentID, incident.EvFlagged, `{"flag":"test_call"}`, "")
	evs, err := s.Events(ctx, o.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := incident.Fold(evs); err != nil || len(p.Flags) != 1 || p.Flags[0] != "test_call" {
		t.Fatalf("stored history folds to flags %v, err %v; want [test_call]", p.Flags, err)
	}

	// The rule is the table's, not the write path's: even the owner, writing
	// below the write path, cannot store a non-string flag. Only flag events
	// carry a flag; another event type is unaffected.
	owner := c.Owner(t, pg.StoreOperational)
	const direct = `INSERT INTO op.incident_events (incident_id, seq, type, actor_kind, payload, idem_key)
		VALUES ($1, $2, $3, 'system', $4::jsonb, $5)`
	if _, err := owner.Exec(ctx, direct, o.IncidentID, 1000, "incident.flagged", `{"flag":123}`, "direct-flag"); pgCode(err) != "23514" {
		t.Errorf("owner insert of a numeric flag: err = %v, want check violation 23514", err)
	}
	if _, err := owner.Exec(ctx, direct, o.IncidentID, 1001, "incident.flagged", `{"note":"no flag"}`, "direct-none"); pgCode(err) != "23514" {
		t.Errorf("owner insert of a flag event without a flag: err = %v, want check violation 23514", err)
	}
	if _, err := owner.Exec(ctx, direct, o.IncidentID, 1002, "incident.escalated", `{"reason":"test","flag":123}`, "direct-other"); err != nil {
		t.Errorf("owner insert of another event type with a non-string flag key: %v", err)
	}
}
