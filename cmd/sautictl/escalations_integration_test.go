//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/platform/pg/pgtest"
)

// F-08: before the M5 console, a coordinator lists the open incidents whose
// escalations nobody has acknowledged (most severe first, every reason, the
// rings so far) and the location call-back tasks, as op_readonly. An
// acknowledged escalation is not listed; no number or free text is printed.
func TestEscalationsListsOnlyUnacknowledged(t *testing.T) {
	t.Setenv("SAUTI_ENV", "dev")
	c := pgtest.Cluster(t)
	ctx := context.Background()
	as := func(role string) *opstore.Store {
		pool, err := pg.Connect(ctx, c.DSN(pg.StoreOperational, role), "test", 2)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return opstore.New(pool)
	}
	in, ivr := as("op_intake"), as("op_ivr")
	open := func() incident.Opened {
		ref, err := incident.NewReference(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		o, err := in.OpenIncident(ctx, incident.OpenParams{Reference: ref, Channel: incident.ChannelUSSDAT, SessionID: uuid.NewV7(),
			ProviderSessionID: "ATUid_" + uuid.NewV4().String(), NetworkCode: "64110", IdemKey: "ussd:at:start",
			Phone: crypto.Sealed{KeyID: "dev:k:2", Ciphertext: []byte{1, 2, 3}}, PhoneMAC: crypto.MAC{KeyID: "dev:k:2", Sum: make([]byte, 32)}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	ev := func(id uuid.UUID, typ incident.EventType, payload, logic string) {
		if _, err := in.Append(ctx, incident.AppendParams{IncidentID: id, Type: typ, Payload: []byte(payload),
			ActorKind: incident.ActorSystem, IdemKey: string(typ) + payload, LogicVersion: logic}); err != nil {
			t.Fatal(err)
		}
	}
	acked, unacked, task := open(), open(), open()
	ev(acked.IncidentID, incident.EvEscalated, `{"reason":"dispatch_stalled"}`, "")
	ev(unacked.IncidentID, incident.EvClassified, `{"severity":"red"}`, "tool1-test+sha256:000000000000")
	ev(unacked.IncidentID, incident.EvEscalated, `{"reason":"no_eligible_responder","role":"vht"}`, "")
	ev(unacked.IncidentID, incident.EvEscalated, `{"reason":"triage_rule_error"}`, "")
	ev(task.IncidentID, incident.EvFlagged, `{"flag":"needs_location_callback"}`, "")
	calls, err := ivr.ClaimCoordinatorCalls(ctx, 2*time.Minute, 10)
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls %+v %v", calls, err)
	}
	for _, cl := range calls {
		if cl.IncidentID == acked.IncidentID {
			if err := ivr.AcknowledgeCoordinatorCall(ctx, cl.IncidentID, cl.Seq); err != nil {
				t.Fatal(err)
			}
		}
	}
	run := func(args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := Main(args, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
		return out.String()
	}
	t.Setenv("SAUTI_DSN", c.DSN(pg.StoreOperational, "op_readonly"))
	text := run("escalations")
	rule, none := strings.Index(text, "triage_rule_error"), strings.Index(text, "no_eligible_responder (vht)")
	if !strings.Contains(text, unacked.Reference.Spoken()) || !strings.Contains(text, "red") || strings.Contains(text, acked.Reference.Spoken()) ||
		rule < 0 || none < rule || !strings.Contains(text, task.Reference.Spoken()) {
		t.Fatalf("text:\n%s", text)
	}
	var got struct {
		Escalations []struct {
			Reference string `json:"reference"`
			Severity  string `json:"severity"`
			Reasons   []struct {
				Reason string `json:"reason"`
				Role   string `json:"role"`
			} `json:"reasons"`
			Rings int `json:"rings"`
		} `json:"escalations"`
		Tasks []struct {
			Reference string `json:"reference"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(run("escalations", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Escalations) != 1 || got.Escalations[0].Reference != unacked.Reference.Spoken() || got.Escalations[0].Rings != 1 ||
		len(got.Escalations[0].Reasons) != 2 || got.Escalations[0].Reasons[0].Reason != "triage_rule_error" ||
		got.Escalations[0].Reasons[1].Role != "vht" || len(got.Tasks) != 1 || got.Tasks[0].Reference != task.Reference.Spoken() {
		t.Fatalf("json %+v", got)
	}
}
