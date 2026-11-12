package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"uuid"

	"sautihealth.com/sauti/internal/dispatch"
)

// Until the SMS adapter exists (Part B), the worker can only simulate
// delivery, which production refuses: a worker that cannot reach responders
// must not run as if it could.
func TestDelivererRefusesProductionWithoutChannels(t *testing.T) {
	if _, err := deliverer("production", slog.Default()); err == nil {
		t.Fatal("production accepted the simulated deliverer")
	}
	var buf bytes.Buffer
	d, err := deliverer("dev", slog.New(slog.NewJSONHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := d.Deliver(context.Background(), dispatch.Delivery{AttemptID: uuid.NewV7(), AlertID: uuid.NewV7(),
		IncidentID: uuid.NewV7(), ResponderID: uuid.NewV7(), Role: "vht", Purpose: dispatch.PurposeAlert, Channel: dispatch.ChannelSMS})
	if err != nil || !strings.HasPrefix(ref, "sim-") || !strings.Contains(buf.String(), `"purpose":"alert"`) {
		t.Fatalf("simulated delivery: %q %v %s", ref, err, buf.String())
	}
}
