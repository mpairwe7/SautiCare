//go:build integration

package opstore_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/responder"
)

// The caller's number for SMS (Build Prompt §10.4): the callback number they
// typed at screen 8 when they gave one, otherwise the number they called
// from. Dispatch gets the sealed value and the row it is bound to, never a
// table it could browse.
func TestCallerContact(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	c, ok, err := r.dispatcher.CallerContact(ctx, r.incident)
	if err != nil || !ok || c.AAD.Table != "sessions" || c.AAD.Column != "phone" || len(c.Sealed.Ciphertext) == 0 {
		t.Fatalf("session contact %+v %v %v", c, ok, err)
	}
	id := uuid.NewV7()
	if _, err := r.intake.StoreIdentifier(ctx, incident.IdentifierParams{ID: id, IncidentID: r.incident, Kind: incident.IdentifierCallback,
		Sealed: crypto.Sealed{KeyID: "dev:k:2", Ciphertext: []byte{9, 9}}, IdemKey: "s8:callback"}); err != nil {
		t.Fatal(err)
	}
	appendEv(t, r.intake, r.incident, incident.EvScreenAnswered,
		`{"screen":8,"field":"callback","value":"provided","identifier_id":"`+id.String()+`"}`, "")
	c, ok, err = r.dispatcher.CallerContact(ctx, r.incident)
	if err != nil || !ok || c.AAD.Table != "incident_identifiers" || c.AAD.RowID != id.String() || string(c.Sealed.Ciphertext) != "\x09\x09" {
		t.Fatalf("callback contact %+v %v %v", c, ok, err)
	}
	conn, err := pg.Connect(ctx, r.c.DSN(pg.StoreOperational, "op_dispatch"), "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, q := range []string{"SELECT phone_ct FROM op.sessions", "SELECT ct FROM op.incident_identifiers"} {
		if _, err := conn.Exec(ctx, q); pgCode(err) != "42501" {
			t.Errorf("op_dispatch: %s: %v", q, err)
		}
	}
}

// Caller notices and responder alerts each claim their SMS attempt once;
// the provider's delivery report lands on the attempt that sent it.
func TestNoticeAttemptsAndDeliveryReports(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	if _, err := r.dispatcher.NotifyCaller(ctx, r.incident, dispatch.CallerHelperOnTheWay, ""); err != nil {
		t.Fatal(err)
	}
	id, fresh, err := r.dispatcher.BeginNoticeAttempt(ctx, r.incident, dispatch.CallerHelperOnTheWay, dispatch.ChannelSMS)
	if err != nil || !fresh {
		t.Fatalf("first claim: %v %v", fresh, err)
	}
	if _, fresh, _ := r.dispatcher.BeginNoticeAttempt(ctx, r.incident, dispatch.CallerHelperOnTheWay, dispatch.ChannelSMS); fresh {
		t.Fatal("a notice attempt was claimed twice")
	}
	if _, fresh, _ := r.dispatcher.BeginNoticeAttempt(ctx, r.incident, dispatch.CallerForwarded, dispatch.ChannelSMS); fresh {
		t.Fatal("an attempt was claimed for a notice that was never recorded")
	}
	if ok, err := r.dispatcher.FinishNoticeAttempt(ctx, id, "sent", "ATXid_notice1"); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	a := r.issue(t, r.incident, r.responder(t, responder.RoleVHT), responder.RoleVHT)
	att, _, _ := r.dispatcher.BeginAttempt(ctx, a.AlertID, dispatch.PurposeAlert, dispatch.ChannelSMS)
	if _, err := r.dispatcher.FinishAttempt(ctx, att, "sent", "ATXid_alert1"); err != nil {
		t.Fatal(err)
	}
	for _, rep := range []opstore.DeliveryReport{
		{ProviderRef: "ATXid_alert1", Status: "Buffered"},
		{ProviderRef: "ATXid_alert1", Status: "Success", Final: true},
		{ProviderRef: "ATXid_notice1", Status: "Failed", FailureReason: "AbsentSubscriber", Final: true},
	} {
		if err := r.intake.RecordDeliveryReport(ctx, rep); err != nil {
			t.Fatalf("%+v: %v", rep, err)
		}
	}
	if err := r.intake.RecordDeliveryReport(ctx, opstore.DeliveryReport{ProviderRef: "ATXid_nobody", Status: "Success", Final: true}); !errors.Is(err, opstore.ErrUnknownDelivery) {
		t.Fatalf("unknown message: %v", err)
	}
	owner := r.c.Owner(t, pg.StoreOperational)
	var alertStatus, noticeStatus, reason string
	if err := owner.QueryRow(ctx, "SELECT delivery_status FROM op.alert_attempts WHERE provider_ref = 'ATXid_alert1'").Scan(&alertStatus); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, "SELECT delivery_status, delivery_failure FROM op.notice_attempts WHERE provider_ref = 'ATXid_notice1'").Scan(&noticeStatus, &reason); err != nil {
		t.Fatal(err)
	}
	if alertStatus != "Success" || noticeStatus != "Failed" || reason != "AbsentSubscriber" {
		t.Fatalf("alert %s, notice %s (%s)", alertStatus, noticeStatus, reason)
	}
	// A final report is not overwritten by a late intermediate one.
	_ = r.intake.RecordDeliveryReport(ctx, opstore.DeliveryReport{ProviderRef: "ATXid_alert1", Status: "Sent"})
	if err := owner.QueryRow(ctx, "SELECT delivery_status FROM op.alert_attempts WHERE provider_ref = 'ATXid_alert1'").Scan(&alertStatus); err != nil || alertStatus != "Success" {
		t.Fatalf("after a late intermediate report: %s %v", alertStatus, err)
	}
}

// Review focus 4: the 8-hour availability-expiry reminder is claimed once
// per period, only when the period ends within the reminder window.
func TestAvailabilityRemindersClaimedOnce(t *testing.T) {
	r := newDispatchRig(t)
	ctx := context.Background()
	soon := r.responder(t, responder.RoleVHT)
	r.responder(t, responder.RoleTransporter) // ends in 8 hours: not yet
	if _, err := r.c.Owner(t, pg.StoreOperational).Exec(ctx, `UPDATE op.availability SET period = tstzrange(lower(period),
		clock_timestamp() + interval '20 minutes', '[)') WHERE responder_id = $1`, soon); err != nil {
		t.Fatal(err)
	}
	got, err := r.dispatcher.ClaimAvailabilityReminders(ctx, 30*time.Minute, 100)
	if err != nil || len(got) != 1 || got[0].ResponderID != soon || time.Until(got[0].Until) > 21*time.Minute {
		t.Fatalf("claimed %+v %v", got, err)
	}
	if again, _ := r.dispatcher.ClaimAvailabilityReminders(ctx, 30*time.Minute, 100); len(again) != 0 {
		t.Fatalf("claimed twice: %+v", again)
	}
}
