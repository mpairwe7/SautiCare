package main

import (
	"context"
	"errors"
	"testing"

	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

type recorderFunc func(context.Context, opstore.DeliveryReport) error

func (f recorderFunc) RecordDeliveryReport(ctx context.Context, r opstore.DeliveryReport) error {
	return f(ctx, r)
}

// The provider's delivery report reaches the store with its codes only; a
// report for a message Sauti never sent is acknowledged as unknown.
func TestDeliveryReportBridge(t *testing.T) {
	var got opstore.DeliveryReport
	b := deliveryReports{recorderFunc(func(_ context.Context, r opstore.DeliveryReport) error { got = r; return nil })}
	if err := b.RecordDeliveryReport(context.Background(), africastalking.DeliveryReport{Provider: "at", MessageID: "ATXid_1",
		Status: "Failed", FailureReason: "AbsentSubscriber", Final: true}); err != nil {
		t.Fatal(err)
	}
	if got != (opstore.DeliveryReport{ProviderRef: "ATXid_1", Status: "Failed", FailureReason: "AbsentSubscriber", Final: true}) {
		t.Fatalf("stored %+v", got)
	}
	unknown := deliveryReports{recorderFunc(func(context.Context, opstore.DeliveryReport) error { return opstore.ErrUnknownDelivery })}
	if err := unknown.RecordDeliveryReport(context.Background(), africastalking.DeliveryReport{MessageID: "x", Status: "Success"}); !errors.Is(err, africastalking.ErrUnknownMessage) {
		t.Fatalf("unknown: %v", err)
	}
}
