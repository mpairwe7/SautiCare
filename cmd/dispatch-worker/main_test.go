package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
)

// Production refuses to start until the key provider exists (P0): the
// worker opens responders' numbers. With Africa's Talking credentials the
// worker sends real SMS; without them, development and tests simulate
// delivery, logging identifiers only.
func TestPickMessenger(t *testing.T) {
	built := false
	real := func() (messenger, error) { built = true; return simMessenger{log: slog.Default()}, nil }
	if _, err := pickMessenger("production", "key", real, slog.Default()); err == nil || built {
		t.Fatal("production accepted")
	}
	if _, err := pickMessenger("dev", "key", real, slog.Default()); err != nil || !built {
		t.Fatalf("credentials given: built=%v %v", built, err)
	}
	var buf bytes.Buffer
	m, err := pickMessenger("dev", "", real, slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := m.Deliver(context.Background(), dispatch.Delivery{AttemptID: uuid.NewV7(), AlertID: uuid.NewV7(),
		IncidentID: uuid.NewV7(), ResponderID: uuid.NewV7(), Role: "vht", Purpose: dispatch.PurposeAlert, Channel: dispatch.ChannelSMS})
	if err != nil || !strings.HasPrefix(ref, "sim-") || !strings.Contains(buf.String(), `"purpose":"alert"`) {
		t.Fatalf("simulated delivery: %q %v %s", ref, err, buf.String())
	}
	if _, err := m.Notify(context.Background(), dispatch.Notice{AttemptID: uuid.NewV7(), IncidentID: uuid.NewV7(), Kind: dispatch.CallerHelperOnTheWay}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remind(context.Background(), dispatch.Reminder{ResponderID: uuid.NewV7(), Until: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// Review I8: the leader is not ready when its last successful pass is older
// than the stall limit (a hang, or the database unreachable); a standby
// instance is ready (it is not dispatching, and it can take over).
func TestReadinessFailsWhenTheLeaderStalls(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		leading bool
		last    time.Time
		ok      bool
	}{
		{false, time.Time{}, true},
		{true, now.Add(-10 * time.Second), true},
		{true, now.Add(-3 * time.Minute), false},
		{true, time.Time{}, false},
	} {
		if err := dispatchLoopReady(tc.leading, tc.last, now); (err == nil) != tc.ok {
			t.Errorf("leading=%v last=%v: %v", tc.leading, tc.last, err)
		}
	}
}
