package africastalking

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type reports struct {
	got []DeliveryReport
	err error
}

func (r *reports) RecordDeliveryReport(_ context.Context, d DeliveryReport) error {
	r.got = append(r.got, d)
	return r.err
}

const drToken = "dr-token-0123456789abcdef0123"

func postReport(t *testing.T, h http.Handler, token string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/sms/delivery/at/"+token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Africa's Talking posts a delivery report per message (id, status,
// failureReason, networkCode, retryCount, phoneNumber). Only codes are kept:
// the phone number is neither passed on nor logged.
func TestDeliveryReportsAreRecordedWithoutTheNumber(t *testing.T) {
	var logs bytes.Buffer
	port := &reports{}
	h, err := NewDeliveryReportHandler(DeliveryReportConfig{Provider: "at", PathToken: drToken}, port, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"id": {"ATXid_abc123"}, "status": {"Failed"}, "failureReason": {"AbsentSubscriber"},
		"networkCode": {"64110"}, "retryCount": {"2"}, "phoneNumber": {"+256772000123"}}
	if rec := postReport(t, h, drToken, form); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	want := DeliveryReport{Provider: "at", MessageID: "ATXid_abc123", Status: "Failed", FailureReason: "AbsentSubscriber", Final: true}
	if len(port.got) != 1 || port.got[0] != want {
		t.Fatalf("recorded %+v, want %+v", port.got, want)
	}
	if strings.Contains(logs.String(), "772000123") {
		t.Fatalf("the number reached the logs: %s", logs.String())
	}
	// Intermediate statuses are recorded but not final.
	form.Set("status", "Buffered")
	form.Del("failureReason")
	postReport(t, h, drToken, form)
	if port.got[1].Final || port.got[1].Status != "Buffered" {
		t.Fatalf("buffered: %+v", port.got[1])
	}
}

// A wrong token is a 404 (the endpoint does not exist for anyone else); a
// malformed report is refused; a report for a message Sauti does not know
// answers 200 (Africa's Talking would otherwise retry a report that can never
// apply); a store error answers 503 so that it is retried.
func TestDeliveryReportsRefuseBadInput(t *testing.T) {
	port := &reports{}
	h, _ := NewDeliveryReportHandler(DeliveryReportConfig{Provider: "at", PathToken: drToken}, port, slog.Default())
	if rec := postReport(t, h, "wrong-token-0123456789abcdef", url.Values{"id": {"x"}, "status": {"Success"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	for _, form := range []url.Values{
		{"status": {"Success"}},
		{"id": {"ATXid_1"}, "status": {"Delivered!"}},
		{"id": {"ATXid_1;DROP"}, "status": {"Success"}},
		{"id": {"ATXid_1"}, "status": {"Failed"}, "failureReason": {"call me on 0772000123"}},
	} {
		if rec := postReport(t, h, drToken, form); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", form, rec.Code)
		}
	}
	if len(port.got) != 0 {
		t.Fatalf("recorded bad input: %+v", port.got)
	}
	port.err = ErrUnknownMessage
	if rec := postReport(t, h, drToken, url.Values{"id": {"ATXid_9"}, "status": {"Success"}}); rec.Code != http.StatusOK {
		t.Fatalf("unknown message: %d", rec.Code)
	}
	port.err = errors.New("database down")
	if rec := postReport(t, h, drToken, url.Values{"id": {"ATXid_9"}, "status": {"Success"}}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store error: %d", rec.Code)
	}
	if _, err := NewDeliveryReportHandler(DeliveryReportConfig{Provider: "at", PathToken: "short"}, port, slog.Default()); err == nil {
		t.Fatal("short token accepted")
	}
}
