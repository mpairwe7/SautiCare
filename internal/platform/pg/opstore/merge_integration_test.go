//go:build integration

package opstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

func dispatchedIncident(t *testing.T, s *opstore.Store) incident.Opened {
	t.Helper()
	return dispatchedAs(t, s, incident.SevRed)
}

func dispatchedAs(t *testing.T, s *opstore.Store, sev incident.Severity) incident.Opened {
	t.Helper()
	o, err := s.OpenIncident(context.Background(), openParams(t))
	if err != nil {
		t.Fatal(err)
	}
	appendEv(t, s, o.IncidentID, incident.EvClassified, `{"severity":"`+string(sev)+`"}`, "tool1-test+sha256:000000000000")
	appendEv(t, s, o.IncidentID, incident.EvDispatchReady, `{"zone":{"col":902,"row":84},"location_quality":"landmark"}`, "")
	return o
}

// absorb merges member into survivor the way the dedup path does: merged on
// the member first, then merge_absorbed on the survivor with the member's
// stored T1.
func absorb(t *testing.T, s *opstore.Store, survivor, member incident.Opened) {
	t.Helper()
	appendEv(t, s, member.IncidentID, incident.EvMerged, fmt.Sprintf(`{"into":%q}`, survivor.IncidentID), "")
	appendEv(t, s, survivor.IncidentID, incident.EvMergeAbsorbed,
		fmt.Sprintf(`{"from":%q,"t1":%q}`, member.IncidentID, member.T1.UTC().Format(time.RFC3339Nano)), "")
}

// The SQL write path projects merge_absorbed, split and unmerged exactly as
// incident.Apply does: survivor T1 = earliest call, recomputed on a split;
// the member revived to dispatching.
func TestMergeSplitParity(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	member, survivor := dispatchedIncident(t, s), dispatchedIncident(t, s) // the member's call came first
	early := member.T1
	absorb(t, s, survivor, member)
	p, err := s.Projection(ctx, survivor.IncidentID)
	if err != nil || !p.T[1].Equal(early) || !early.Before(survivor.T1) || len(p.Absorbed) != 1 {
		t.Fatalf("after absorb: %+v %v", p, err)
	}
	// Only the console splits (I6): split and unmerged through op.split_incident.
	if err := storeAs(t, c, "op_console").SplitIncident(ctx, survivor.IncidentID, member.IncidentID, uuid.NewV7()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{survivor.IncidentID, member.IncidentID} {
		evs, err := s.Events(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want, err := incident.Fold(evs)
		if err != nil {
			t.Fatalf("fold %s: %v", id, err)
		}
		got, err := s.Projection(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d := incident.Diff(got, want); d != "" {
			t.Fatalf("%s: DB projection differs from Fold: %s", id, d)
		}
	}
	sp, _ := s.Projection(ctx, survivor.IncidentID)
	mp, _ := s.Projection(ctx, member.IncidentID)
	if !sp.T[1].Equal(survivor.T1) || len(sp.Absorbed) != 0 || mp.State != incident.StateDispatching || mp.MergedInto != nil {
		t.Fatalf("after split: survivor %+v member %+v", sp, mp)
	}
}

// storeAsSuperuser connects to the operational store as the test server's
// superuser, a member of every role (tests of checks behind a role gate).
func storeAsSuperuser(t *testing.T, c *pgtest.TestCluster) *opstore.Store {
	t.Helper()
	u, err := url.Parse(pgtest.AdminURL(t))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + c.Database(pg.StoreOperational)
	pool, err := pg.Connect(context.Background(), u.String(), "test-superuser", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return opstore.New(pool)
}

// I6: "only a coordinator splits" is enforced by the database. Intake (and
// any role but the console) is refused a staff actor, incident.split and
// incident.unmerged on the generic append, whatever the payload; the
// console splits through op.split_incident.
func TestOnlyTheConsoleRecordsStaffActions(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	survivor, member := dispatchedIncident(t, s), dispatchedIncident(t, s)
	appendEv(t, s, member.IncidentID, incident.EvMerged, fmt.Sprintf(`{"into":%q}`, survivor.IncidentID), "")
	appendEv(t, s, survivor.IncidentID, incident.EvMergeAbsorbed,
		fmt.Sprintf(`{"from":%q,"t1":%q}`, member.IncidentID, member.T1.UTC().Format(time.RFC3339Nano)), "")
	staff := uuid.NewV7()
	for i, p := range []incident.AppendParams{
		{IncidentID: survivor.IncidentID, Type: incident.EvSplit, Payload: json.RawMessage(fmt.Sprintf(`{"from":%q}`, member.IncidentID)),
			ActorKind: incident.ActorStaff, StaffID: &staff},
		{IncidentID: survivor.IncidentID, Type: incident.EvSplit, Payload: json.RawMessage(fmt.Sprintf(`{"from":%q}`, member.IncidentID)),
			ActorKind: incident.ActorSystem},
		{IncidentID: member.IncidentID, Type: incident.EvUnmerged, Payload: json.RawMessage(fmt.Sprintf(`{"from":%q}`, survivor.IncidentID)),
			ActorKind: incident.ActorStaff, StaffID: &staff},
		{IncidentID: member.IncidentID, Type: incident.EvUnmerged, Payload: json.RawMessage(fmt.Sprintf(`{"from":%q}`, survivor.IncidentID)),
			ActorKind: incident.ActorSystem},
		{IncidentID: survivor.IncidentID, Type: incident.EvFlagged, Payload: json.RawMessage(`{"flag":"reviewed"}`),
			ActorKind: incident.ActorStaff, StaffID: &staff},
	} {
		p.IdemKey = fmt.Sprintf("forged-%d", i)
		if _, err := s.Append(ctx, p); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("op_intake appended %s as %s: %v", p.Type, p.ActorKind, err)
		}
	}
	if mp, _ := s.Projection(ctx, member.IncidentID); mp.State != incident.StateMerged {
		t.Fatalf("member revived by intake: %+v", mp)
	}
	if err := storeAs(t, c, "op_console").SplitIncident(ctx, survivor.IncidentID, member.IncidentID, staff); err != nil {
		t.Fatalf("console split: %v", err)
	}
	if mp, _ := s.Projection(ctx, member.IncidentID); mp.State != incident.StateDispatching {
		t.Fatalf("member after the console split: %+v", mp)
	}
}

// C2: the SQL write path projects severity_raised exactly as Apply does
// (severity only, not the logic version) and refuses what Apply refuses. It
// also checks what Apply cannot see: the "from" call's own row is merged into
// this survivor and carries exactly the raised severity.
func TestSeverityRaisedParity(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	o := dispatchedAs(t, s, incident.SevGreen)
	yellow, red := dispatchedAs(t, s, incident.SevYellow), dispatchedAs(t, s, incident.SevRed)
	absorb(t, s, o, yellow)
	absorb(t, s, o, red)
	stranger := dispatchedAs(t, s, incident.SevRed) // a real red call never merged into o
	from := yellow.IncidentID
	for i, bad := range []string{
		fmt.Sprintf(`{"severity":"green","from":%q}`, from),
		fmt.Sprintf(`{"severity":"purple","from":%q}`, from),
		`{"severity":"red"}`,
		`{"severity":"red","from":"x"}`,
		fmt.Sprintf(`{"severity":"red","from":%q}`, o.IncidentID),
		fmt.Sprintf(`{"severity":"red","from":%q,"why":"x"}`, from),
		fmt.Sprintf(`{"severity":3,"from":%q}`, from),
		fmt.Sprintf(`{"severity":"red","from":%q}`, uuid.NewV7()),        // no such call
		fmt.Sprintf(`{"severity":"red","from":%q}`, stranger.IncidentID), // a call never merged into o
		fmt.Sprintf(`{"severity":"red","from":%q}`, from),                // more than the yellow call carries
	} {
		if _, err := s.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: incident.EvSeverityRaised,
			Payload: json.RawMessage(bad), ActorKind: incident.ActorSystem, IdemKey: fmt.Sprintf("bad-%d", i)}); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
	appendEv(t, s, o.IncidentID, incident.EvSeverityRaised, fmt.Sprintf(`{"severity":"yellow","from":%q}`, yellow.IncidentID), "")
	appendEv(t, s, o.IncidentID, incident.EvSeverityRaised, fmt.Sprintf(`{"severity":"red","from":%q}`, red.IncidentID), "")
	evs, err := s.Events(ctx, o.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := incident.Fold(evs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Projection(ctx, o.IncidentID)
	if err != nil {
		t.Fatal(err)
	}
	if d := incident.Diff(got, want); d != "" || got.Severity != incident.SevRed || got.LogicVersion != "tool1-test+sha256:000000000000" {
		t.Fatalf("projection %+v; diff from Fold: %s", got, d)
	}
	if _, err := s.Append(ctx, incident.AppendParams{IncidentID: o.IncidentID, Type: incident.EvSeverityRaised,
		Payload: json.RawMessage(fmt.Sprintf(`{"severity":"red","from":%q}`, red.IncidentID)), ActorKind: incident.ActorSystem,
		IdemKey: "same"}); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("raise to the current severity: %v", err)
	}
}

// merge_absorbed names a call merged into this survivor and carries that
// call's stored T1: intake cannot attach an unrelated or unknown call, or
// lower the survivor's T1 (the start of the primary outcome) with a forged
// timestamp. Apply sees one incident, so these checks are the database's.
func TestMergeAbsorbedMatchesTheMergedCall(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	survivor, member, elsewhere := dispatchedIncident(t, s), dispatchedIncident(t, s), dispatchedIncident(t, s)
	t1 := member.T1.UTC().Format(time.RFC3339Nano)
	try := func(key, from, t1 string) error {
		_, err := s.Append(ctx, incident.AppendParams{IncidentID: survivor.IncidentID, Type: incident.EvMergeAbsorbed,
			Payload: json.RawMessage(fmt.Sprintf(`{"from":%q,"t1":%q}`, from, t1)), ActorKind: incident.ActorSystem, IdemKey: key})
		return err
	}
	if err := try("unknown", uuid.NewV7().String(), t1); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("absorbing an unknown call: %v", err)
	}
	if err := try("not-merged", member.IncidentID.String(), t1); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("absorbing a call not merged: %v", err)
	}
	appendEv(t, s, member.IncidentID, incident.EvMerged, fmt.Sprintf(`{"into":%q}`, elsewhere.IncidentID), "")
	if err := try("merged-elsewhere", member.IncidentID.String(), t1); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("absorbing a call merged into another incident: %v", err)
	}
	other := dispatchedIncident(t, s)
	appendEv(t, s, other.IncidentID, incident.EvMerged, fmt.Sprintf(`{"into":%q}`, survivor.IncidentID), "")
	forged := other.T1.Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if err := try("forged-t1", other.IncidentID.String(), forged); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("absorbing a merged call with a forged earlier T1: %v", err)
	}
	if err := try("ok", other.IncidentID.String(), other.T1.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("absorbing the merged call with its own T1: %v", err)
	}
	p, err := s.Projection(ctx, survivor.IncidentID)
	if err != nil || !p.T[1].Equal(survivor.T1) || len(p.Absorbed) != 1 {
		t.Fatalf("survivor after the absorb: %+v %v", p, err)
	}
}

// Whatever Apply refuses for merge, split and unmerge, the database refuses.
func TestSQLRejectsInvalidMergeSplit(t *testing.T) {
	c := pgtest.Cluster(t)
	s := storeAs(t, c, "op_intake")
	ctx := context.Background()
	survivor, member := dispatchedIncident(t, s), dispatchedIncident(t, s)
	staff := uuid.NewV7()
	// Staff events reach the generic append only from a console session
	// (I6); a superuser stands in for it to reach the content checks below.
	privileged := storeAsSuperuser(t, c)
	try := func(id uuid.UUID, typ incident.EventType, payload, actor string, key string) error {
		p := incident.AppendParams{IncidentID: id, Type: typ, Payload: json.RawMessage(payload), ActorKind: actor, IdemKey: key}
		via := s
		if actor == incident.ActorStaff {
			p.StaffID, via = &staff, privileged
		}
		_, err := via.Append(ctx, p)
		return err
	}
	cases := []struct {
		id      uuid.UUID
		typ     incident.EventType
		payload string
		actor   string
	}{
		{survivor.IncidentID, incident.EvMergeAbsorbed, `{"from":"x","t1":"2026-11-12T10:00:00Z"}`, incident.ActorSystem},
		{survivor.IncidentID, incident.EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"yesterday"}`, member.IncidentID), incident.ActorSystem},
		{survivor.IncidentID, incident.EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"2026-11-12T10:00:00.1234567Z"}`, member.IncidentID), incident.ActorSystem},
		{survivor.IncidentID, incident.EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"2026-11-12T10:00:00Z","x":1}`, member.IncidentID), incident.ActorSystem},
		{survivor.IncidentID, incident.EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":"2026-11-12T10:00:00Z"}`, survivor.IncidentID), incident.ActorSystem},
		{survivor.IncidentID, incident.EvSplit, fmt.Sprintf(`{"from":%q}`, member.IncidentID), incident.ActorStaff},
		{member.IncidentID, incident.EvUnmerged, fmt.Sprintf(`{"from":%q}`, survivor.IncidentID), incident.ActorStaff},
	}
	for i, tc := range cases {
		if err := try(tc.id, tc.typ, tc.payload, tc.actor, fmt.Sprintf("bad-%d", i)); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("case %d %s %s: err = %v", i, tc.typ, tc.payload, err)
		}
	}
	appendEv(t, s, member.IncidentID, incident.EvMerged, fmt.Sprintf(`{"into":%q}`, survivor.IncidentID), "")
	memberT1 := member.T1.UTC().Format(time.RFC3339Nano)
	if err := try(survivor.IncidentID, incident.EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":%q}`, member.IncidentID, memberT1),
		incident.ActorSystem, "ok-absorb"); err != nil {
		t.Fatal(err)
	}
	if err := try(survivor.IncidentID, incident.EvMergeAbsorbed, fmt.Sprintf(`{"from":%q,"t1":%q}`, member.IncidentID, memberT1),
		incident.ActorSystem, "absorb-twice"); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("absorbing the same call twice: %v", err)
	}
	if err := try(survivor.IncidentID, incident.EvSplit, fmt.Sprintf(`{"from":%q}`, member.IncidentID), incident.ActorSystem, "split-system"); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("split by the system: %v", err)
	}
	for i, typ := range []incident.EventType{incident.EvFlagged, incident.EvClosed, incident.EvEscalated} {
		if err := try(member.IncidentID, typ, `{"flag":"x","reason":"x"}`, incident.ActorSystem, fmt.Sprintf("m-%d", i)); !errors.Is(err, incident.ErrIllegalTransition) {
			t.Errorf("%s on a merged incident: %v", typ, err)
		}
	}
	if err := try(member.IncidentID, incident.EvUnmerged, fmt.Sprintf(`{"from":%q}`, uuid.NewV7()), incident.ActorStaff, "wrong-from"); !errors.Is(err, incident.ErrIllegalTransition) {
		t.Errorf("unmerged from the wrong survivor: %v", err)
	}
}
