//go:build integration

package opstore_test

import (
	"context"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
	"sautihealth.com/sauti/internal/responder"
)

func registerOne(t *testing.T, c *pgtest.TestCluster, ids crypto.Identifiers, role responder.Role) (*responder.Service, responder.Registered) {
	t.Helper()
	gazetteerVersion(t, c) // a responder registers at a stage of the gazetteer in force
	svc := responder.NewService(storeAs(t, c, "op_console"), ids)
	phone, err := pii.ParsePhone("0772000123")
	if err != nil {
		t.Fatal(err)
	}
	cert, ins := time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour)
	reg, err := svc.Register(context.Background(), responder.RegisterCmd{Role: role, Name: pii.NewPersonName("Test Rider"),
		Phone: phone, Ward: "w01", Stage: "w01_stage", CertifiedAt: &cert, InsuredUntil: &ins, KitOK: true})
	if err != nil {
		t.Fatal(err)
	}
	return svc, reg
}

// A responder's name and number are sealed to their own row and found again
// only through the keyed phone hash; dispatch reads the registry without them.
func TestRegisterResponderSealsIdentifiers(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	ids := identifiers(t)
	_, reg := registerOne(t, c, ids, responder.RoleTransporter)
	var nameCT, phoneCT []byte
	var nameKey, phoneKey string
	if err := c.Owner(t, pg.StoreOperational).QueryRow(ctx, "SELECT name_ct, name_key_id, phone_ct, phone_key_id FROM op.responders WHERE id = $1",
		reg.ID).Scan(&nameCT, &nameKey, &phoneCT, &phoneKey); err != nil {
		t.Fatal(err)
	}
	aad := crypto.AAD{Store: "operational", Table: "responders", Column: "phone", RowID: reg.ID.String()}
	if got, err := ids.Env.Open(ctx, crypto.Sealed{KeyID: phoneKey, Ciphertext: phoneCT}, aad); err != nil || string(got) != "+256772000123" {
		t.Fatalf("phone opens to %q, %v", got, err)
	}
	aad.Column = "name"
	if _, err := ids.Env.Open(ctx, crypto.Sealed{KeyID: nameKey, Ciphertext: phoneCT}, aad); err == nil {
		t.Fatal("phone ciphertext opened as the name")
	}
	dispatch := storeAs(t, c, "op_dispatch")
	rs, err := dispatch.Responders(ctx)
	if err != nil || len(rs) != 1 || rs[0].ID != reg.ID || rs[0].Role != responder.RoleTransporter || rs[0].Stage != "w01_stage" ||
		rs[0].Gates(time.Now()) != nil {
		t.Fatalf("registry as dispatch sees it: %+v %v", rs, err)
	}
	ph, _ := pii.ParsePhone("+256772000123")
	macs, _ := ids.PhoneLookupMACs(ctx, ph)
	if id, ok, err := storeAs(t, c, "op_intake").ResponderByPhone(ctx, macs); err != nil || !ok || id != reg.ID {
		t.Fatalf("lookup by phone: %v %v %v", id, ok, err)
	}
}

// Availability periods never overlap (WITHOUT OVERLAPS), last 8 hours on the
// database clock, renew by closing the open period, and end on "off".
func TestAvailabilityPeriods(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	_, reg := registerOne(t, c, identifiers(t), responder.RoleVHT)
	intake := storeAs(t, c, "op_intake")
	p1, err := intake.SetAvailability(ctx, reg.ID, true, "ussd")
	if err != nil || p1.Until.Sub(p1.From) != responder.AvailabilityTTL {
		t.Fatalf("on: %+v %v", p1, err)
	}
	p2, err := intake.SetAvailability(ctx, reg.ID, true, "ussd")
	if err != nil || p2.From.Before(p1.From) || p2.Until.Sub(p2.From) != responder.AvailabilityTTL {
		t.Fatalf("renew: %+v %v", p2, err)
	}
	rs, _ := storeAs(t, c, "op_dispatch").Responders(ctx)
	if !rs[0].AvailableAt(time.Now()) {
		t.Fatal("not available after renewal")
	}
	if _, err := intake.SetAvailability(ctx, reg.ID, false, "ussd"); err != nil {
		t.Fatal(err)
	}
	rs, _ = storeAs(t, c, "op_dispatch").Responders(ctx)
	if rs[0].AvailableAt(time.Now().Add(time.Second)) {
		t.Fatal("still available after off")
	}
	owner := c.Owner(t, pg.StoreOperational)
	if _, err := owner.Exec(ctx, "INSERT INTO op.availability (responder_id, period, source) VALUES ($1, tstzrange(now() - interval '1 hour', now() + interval '1 hour'), 'console')",
		reg.ID); err == nil {
		t.Fatal("overlapping availability accepted")
	}
	var n int
	if err := owner.QueryRow(ctx, "SELECT count(*) FROM op.availability WHERE responder_id = $1", reg.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("periods = %d, %v (first renewed, second closed)", n, err)
	}
}

// Only the console registers responders or changes their gates; runtime
// roles cannot write the registry directly, and the read-only verifier never
// sees identifiers.
func TestRegistryWritesAreConsoleOnly(t *testing.T) {
	c := pgtest.Cluster(t)
	ctx := context.Background()
	_, reg := registerOne(t, c, identifiers(t), responder.RoleVHT)
	if err := storeAs(t, c, "op_intake").SetGates(ctx, reg.ID, responder.Gates{KitOK: false}); err == nil {
		t.Fatal("op_intake changed gates")
	}
	if err := storeAs(t, c, "op_console").SetGates(ctx, reg.ID, responder.Gates{KitOK: false, Status: responder.StatusActive}); err != nil {
		t.Fatal(err)
	}
	for role, q := range map[string]string{
		"op_intake":   "UPDATE op.responders SET kit_ok = true",
		"op_dispatch": "DELETE FROM op.availability",
		"op_readonly": "SELECT phone_ct FROM op.responders",
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
	_ = opstore.ErrNoGazetteer
}
