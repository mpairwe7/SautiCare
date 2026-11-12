package intake

import (
	"context"
	"encoding/json"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
)

type stalledReader struct {
	ids   []uuid.UUID
	after time.Duration
}

func (s *stalledReader) ListStalledDispatch(_ context.Context, after time.Duration) ([]uuid.UUID, error) {
	s.after = after
	return s.ids, nil
}

type watchdogLog struct{ got []incident.AppendParams }

func (a *watchdogLog) Append(_ context.Context, p incident.AppendParams) (incident.Appended, error) {
	a.got = append(a.got, p)
	return incident.Appended{}, nil
}

// Review I8: dispatch-worker may be down or stuck. intake-gw's leader
// (another process) escalates every incident dispatched more than the
// watchdog time ago with no alert and no dispatch escalation at all, once,
// with the dispatch escalation key, so a coordinator takes over.
func TestWatchdogEscalatesStalledDispatch(t *testing.T) {
	a, b := uuid.NewV7(), uuid.NewV7()
	r := &stalledReader{ids: []uuid.UUID{a, b}}
	log := &watchdogLog{}
	n, err := NewWatchdog(r, log, 2*time.Minute).RunOnce(context.Background())
	if err != nil || n != 2 || r.after != 2*time.Minute || len(log.got) != 2 {
		t.Fatalf("n=%d err=%v after=%v appended=%d", n, err, r.after, len(log.got))
	}
	for i, id := range []uuid.UUID{a, b} {
		p := log.got[i]
		var pl struct {
			Reason string `json:"reason"`
		}
		if p.IncidentID != id || p.Type != incident.EvEscalated || json.Unmarshal(p.Payload, &pl) != nil ||
			pl.Reason != ReasonDispatchStalled || p.IdemKey != incident.EscalationKey(ReasonDispatchStalled, "") {
			t.Fatalf("append %d = %+v (%s)", i, p, p.Payload)
		}
	}
}
