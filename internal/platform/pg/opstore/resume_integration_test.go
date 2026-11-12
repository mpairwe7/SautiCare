//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// A redial within the window resumes the same incident through a new session
// row and a session.resumed event; T1 is unchanged.
func TestResumeSessionAppendsResumedEvent(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	op := openParams(t)
	o, err := s.OpenIncident(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	rp := incident.ResumeParams{IncidentID: o.IncidentID, Channel: incident.ChannelIVR, SessionID: uuid.NewV7(),
		ProviderSessionID: "call-1", IdemKey: "ivr:call-1:start", Phone: op.Phone, PhoneMAC: op.PhoneMAC}
	r, err := s.ResumeSession(ctx, rp)
	if err != nil || !r.Created || r.IncidentID != o.IncidentID || r.Reference != o.Reference || !r.T1.Equal(o.T1) {
		t.Fatalf("resume = %+v, %v", r, err)
	}
	again, err := s.ResumeSession(ctx, rp)
	if err != nil || again.Created || again.IncidentID != o.IncidentID {
		t.Fatalf("retried resume = %+v, %v", again, err)
	}
	evs, err := s.Events(ctx, o.IncidentID)
	if err != nil || len(evs) != 2 || evs[1].Type != incident.EvSessionResumed {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	var sessions int
	if err := c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT count(*) FROM op.sessions WHERE incident_id = $1", o.IncidentID).Scan(&sessions); err != nil || sessions != 2 {
		t.Fatalf("sessions = %d, %v", sessions, err)
	}
}

// Only an unfinished, open incident whose last activity is inside the window
// is resumable, and only by its caller's number (any key period).
func TestFindResumable(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	op := openParams(t)
	op.PhoneMAC = crypto.MAC{KeyID: "dev:k:2", Sum: []byte("0123456789abcdef0123456789abcdef")}
	o, err := s.OpenIncident(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	other := crypto.MAC{KeyID: "dev:k:1", Sum: make([]byte, 32)}
	if id, ok, err := s.FindResumable(ctx, []crypto.MAC{other, op.PhoneMAC}, 10*time.Minute); err != nil || !ok || id != o.IncidentID {
		t.Fatalf("within window: %s %v %v", id, ok, err)
	}
	if _, ok, _ := s.FindResumable(ctx, []crypto.MAC{other}, 10*time.Minute); ok {
		t.Fatal("resumed with another caller's MAC")
	}
	owner := c.Owner(t, pg.StoreOperational)
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET updated_at = clock_timestamp() - interval '11 minutes' WHERE id = $1", o.IncidentID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FindResumable(ctx, []crypto.MAC{op.PhoneMAC}, 10*time.Minute); ok {
		t.Fatal("resumed after the window")
	}
	// Tool 1 finished but T2 never recorded (a request failed in between):
	// the redial resumes it, so T2 is recorded (C1).
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET updated_at = clock_timestamp(), last_screen = 8 WHERE id = $1", o.IncidentID); err != nil {
		t.Fatal(err)
	}
	if id, ok, _ := s.FindResumable(ctx, []crypto.MAC{op.PhoneMAC}, 10*time.Minute); !ok || id != o.IncidentID {
		t.Fatal("a finished Tool 1 without T2 is not resumable")
	}
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET t2 = clock_timestamp() WHERE id = $1", o.IncidentID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FindResumable(ctx, []crypto.MAC{op.PhoneMAC}, 10*time.Minute); ok {
		t.Fatal("resumed a completed, dispatched Tool 1")
	}
	if _, err := owner.Exec(ctx, "UPDATE op.incidents SET last_screen = 3, state = 'closed' WHERE id = $1", o.IncidentID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FindResumable(ctx, []crypto.MAC{op.PhoneMAC}, 10*time.Minute); ok {
		t.Fatal("resumed a closed incident")
	}
	if _, ok, err := s.FindResumable(ctx, nil, 10*time.Minute); ok || err != nil {
		t.Fatalf("no MACs (withheld number): %v %v", ok, err)
	}
}
