package africastalking

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/egress"
)

func smsServer(t *testing.T, status int, body string, seen *url.Values, key *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/version1/messaging" || r.Header.Get("Accept") != "application/json" {
			http.Error(w, "bad request shape", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if seen != nil {
			*seen = r.PostForm
		}
		if key != nil {
			*key = r.Header.Get("apiKey")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func recipients(code int, status, id string) string {
	return fmt.Sprintf(`{"SMSMessageData":{"Message":"Sent to 1/1","Recipients":[{"statusCode":%d,"number":"+256772000123","status":%q,"cost":"UGX 35","messageId":%q}]}}`,
		code, status, id)
}

func newSMS(t *testing.T, base string) *SMSClient {
	t.Helper()
	u, _ := url.Parse(base)
	c, err := NewSMSClient(SMSConfig{BaseURL: base, Username: "sandbox", APIKey: "k-" + t.Name(), SenderID: "SAUTI",
		HTTP: egress.NewClient([]string{u.Hostname()}, 2*time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var to, _ = pii.ParsePhone("0772000123")

// The request is Africa's Talking's documented form (username, to, message,
// from) with the API key in the apiKey header; the message ID comes back.
func TestSMSSendsTheDocumentedRequest(t *testing.T) {
	var form url.Values
	var key string
	srv := smsServer(t, http.StatusCreated, recipients(101, "Success", "ATXid_abc123"), &form, &key)
	id, err := newSMS(t, srv.URL).Send(context.Background(), to, "Sauti alert 123 456 7890")
	if err != nil || id != "ATXid_abc123" {
		t.Fatalf("Send: %q %v", id, err)
	}
	if form.Get("username") != "sandbox" || form.Get("to") != "+256772000123" || form.Get("message") != "Sauti alert 123 456 7890" ||
		form.Get("from") != "SAUTI" || key != "k-"+t.Name() {
		t.Fatalf("request form %v key %q", form, key)
	}
}

// Accepted (100 Processed, 101 Sent, 102 Queued) is a send; a refusal by the
// provider (401–409, 502) is ErrSMSRejected — nothing went out; anything
// else (a 500 or 501 status, a server error, a timeout, an unreadable
// answer) is an unknown outcome that must never be retried blindly.
func TestSMSStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		http     int
		body     string
		rejected bool
		ok       bool
	}{
		{201, recipients(100, "Processed", "a"), false, true},
		{201, recipients(102, "Queued", "b"), false, true},
		{201, recipients(403, "InvalidPhoneNumber", "None"), true, false},
		{201, recipients(406, "UserInBlacklist", "None"), true, false},
		{201, recipients(502, "RejectedByGateway", "None"), true, false},
		{201, recipients(500, "InternalServerError", "None"), false, false},
		{401, `The supplied authentication is invalid`, true, false},
		{500, `oops`, false, false},
		{201, `not json`, false, false},
		{201, `{"SMSMessageData":{"Recipients":[]}}`, false, false},
	} {
		srv := smsServer(t, tc.http, tc.body, nil, nil)
		id, err := newSMS(t, srv.URL).Send(context.Background(), to, "x")
		switch {
		case tc.ok && (err != nil || id == ""):
			t.Errorf("%d %s: %q %v, want sent", tc.http, tc.body, id, err)
		case tc.rejected && !errors.Is(err, ErrSMSRejected):
			t.Errorf("%d %s: %v, want rejected", tc.http, tc.body, err)
		case !tc.ok && !tc.rejected && (err == nil || errors.Is(err, ErrSMSRejected)):
			t.Errorf("%d %s: %v, want an unknown outcome", tc.http, tc.body, err)
		}
	}
}

// A slow provider is cut off by the caller's deadline: unknown, not sent.
func TestSMSTimeoutIsUnknown(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := newSMS(t, srv.URL).Send(ctx, to, "x"); err == nil || errors.Is(err, ErrSMSRejected) {
		t.Fatalf("timeout: %v", err)
	}
}

// Neither the number nor the key ever appears in an error (they reach logs).
func TestSMSErrorsCarryNoNumberOrKey(t *testing.T) {
	srv := smsServer(t, 201, recipients(403, "InvalidPhoneNumber", "None"), nil, nil)
	_, err := newSMS(t, srv.URL).Send(context.Background(), to, "x")
	if err == nil || strings.Contains(err.Error(), "772000123") || strings.Contains(err.Error(), "k-") {
		t.Fatalf("error %v", err)
	}
}

func TestNewSMSClientValidates(t *testing.T) {
	for _, c := range []SMSConfig{
		{BaseURL: "http://x", Username: "u", HTTP: http.DefaultClient},             // no key
		{BaseURL: "ftp://x", Username: "u", APIKey: "k", HTTP: http.DefaultClient}, // bad scheme
		{BaseURL: "https://x", APIKey: "k", HTTP: http.DefaultClient},              // no username
		{BaseURL: "https://x", Username: "u", APIKey: "k"},                         // no egress client
		{BaseURL: "https://x", Username: "u", APIKey: "k", SenderID: "TOO-LONG-SENDER-ID", HTTP: http.DefaultClient},
	} {
		if _, err := NewSMSClient(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}
