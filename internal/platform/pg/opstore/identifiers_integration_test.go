//go:build integration

package opstore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"testing"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func identifiers(t *testing.T) crypto.Identifiers {
	t.Helper()
	dir := t.TempDir() + "/keys"
	if err := devkeys.Generate(dir, rand.Reader); err != nil {
		t.Fatal(err)
	}
	keys, err := devkeys.Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return crypto.Identifiers{Env: crypto.NewEnvelope(keys, rand.Reader)}
}

// A sealed identifier is stored once per (incident, idempotency key), opens
// only under its own row's AAD, and no runtime role can change or delete it;
// the read-only verifier cannot even read it.
func TestStoreIdentifierIsIdempotentAndBound(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	ids := identifiers(t)
	svc := incident.NewService(s, ids, rand.Reader)
	id1, err := svc.StoreIdentifier(ctx, o.IncidentID, incident.IdentifierCallback, []byte("+256772123456"), "ussd:at:s1:s8")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc.StoreIdentifier(ctx, o.IncidentID, incident.IdentifierCallback, []byte("+256772123456"), "ussd:at:s1:s8")
	if err != nil || id2 != id1 {
		t.Fatalf("retry: id %s (first %s), err %v", id2, id1, err)
	}
	owner := c.Owner(t, pg.StoreOperational)
	var n int
	var ct []byte
	var keyID string
	if err := owner.QueryRow(ctx, "SELECT count(*) OVER (), ct, key_id FROM op.incident_identifiers WHERE incident_id = $1",
		o.IncidentID).Scan(&n, &ct, &keyID); err != nil || n != 1 {
		t.Fatalf("rows = %d, err = %v", n, err)
	}
	aad := crypto.AAD{Store: "operational", Table: "incident_identifiers", Column: "ct", RowID: id1.String()}
	if got, err := ids.Env.Open(ctx, crypto.Sealed{KeyID: keyID, Ciphertext: ct}, aad); err != nil || string(got) != "+256772123456" {
		t.Fatalf("open = %q, %v", got, err)
	}
	moved := aad
	moved.RowID = o.IncidentID.String()
	if _, err := ids.Env.Open(ctx, crypto.Sealed{KeyID: keyID, Ciphertext: ct}, moved); err == nil {
		t.Fatal("ciphertext opened under another row")
	}
	for role, q := range map[string]string{
		"op_intake":   "UPDATE op.incident_identifiers SET kind = 'callback'",
		"op_ivr":      "DELETE FROM op.incident_identifiers",
		"op_readonly": "SELECT ct FROM op.incident_identifiers",
	} {
		conn, err := pg.Connect(ctx, c.DSN(pg.StoreOperational, role), "test", 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, q); err == nil {
			t.Errorf("%s: %s succeeded", role, q)
		}
		conn.Close()
	}
}

// Step keys are keyed MACs with the consumed input length; a lookup with
// several candidate MACs (current and previous key period) finds the row,
// and Latest returns the furthest step of the session.
func TestStepStoreKeyedByMAC(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o, err := s.OpenIncident(ctx, openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	ids := identifiers(t)
	for i, text := range []string{"", "1", "1*2"} {
		mac, err := ids.StepMAC(ctx, []byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Save(ctx, "at", "S1", intake.Step{Screen: intake.Screen{Text: "screen " + text}, ConsumedLen: len(text),
			MAC: mac, IncidentID: o.IncidentID, Kind: intake.KindDanger}); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	mac, _ := ids.StepMAC(ctx, []byte("1"))
	decoy := crypto.MAC{KeyID: "dev:old", Sum: make([]byte, 32)}
	st, ok, err := s.Lookup(ctx, "at", "S1", []crypto.MAC{decoy, mac})
	if err != nil || !ok || st.Screen.Text != "screen 1" || st.ConsumedLen != 1 {
		t.Fatalf("Lookup = %+v %v %v", st, ok, err)
	}
	latest, ok, err := s.Latest(ctx, "at", "S1")
	if err != nil || !ok || latest.ConsumedLen != 3 || latest.Screen.Text != "screen 1*2" || latest.IncidentID != o.IncidentID ||
		latest.Kind != intake.KindDanger {
		t.Fatalf("Latest = %+v %v %v", latest, ok, err)
	}
	if _, ok, _ := s.Latest(ctx, "at", "S2"); ok {
		t.Fatal("Latest found a step of another session")
	}
	rows, err := c.Owner(t, pg.StoreOperational).Query(ctx, "SELECT input_mac FROM op.ussd_steps")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var m []byte
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"", "1", "1*2"} {
			if plain := sha256.Sum256([]byte(text)); bytes.Equal(m, plain[:]) {
				t.Fatal("a step key is the plain sha256 of its input")
			}
		}
	}
}
