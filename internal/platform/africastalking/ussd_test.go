package africastalking

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sautihealth.com/sauti/internal/intake"
)

type fakePort struct {
	got    []intake.USSDRequest
	screen intake.Screen
	err    error
}

func (f *fakePort) HandleUSSD(_ context.Context, r intake.USSDRequest) (intake.Screen, error) {
	f.got = append(f.got, r)
	return f.screen, f.err
}

const token = "s3cr3t-path-token-0123456789"

func newHandler(t *testing.T, p *fakePort) http.Handler {
	t.Helper()
	h, err := NewUSSDHandler(USSDConfig{
		Provider: "at", PathToken: token, ServiceCodes: []string{"*384*1#"},
		MaxBody: 8 << 10, ErrorText: "Sorry. Call 0800 100 200 free.",
	}, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func post(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func form(text string) url.Values {
	return url.Values{"sessionId": {"ATUid_42"}, "serviceCode": {"*384*1#"}, "phoneNumber": {"+256772123456"},
		"text": {text}, "networkCode": {"64110"}}
}

func TestATParsesFormAndRendersCONEND(t *testing.T) {
	p := &fakePort{screen: intake.Screen{Text: "Choose language\n1 English\n2 Luganda", End: false}}
	h := newHandler(t, p)
	rec := post(h, "/v1/ussd/at/"+token, form("1*2"))
	if rec.Code != http.StatusOK || rec.Body.String() != "CON Choose language\n1 English\n2 Luganda" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	r := p.got[0]
	if r.Provider != "at" || r.SessionID != "ATUid_42" || r.Text != "1*2" || r.NetworkCode != "64110" || r.Phone.E164() != "+256772123456" {
		t.Fatalf("parsed request = %+v", r)
	}
	p.screen.End = true
	if rec := post(h, "/v1/ussd/at/"+token, form("")); rec.Body.String() != "END Choose language\n1 English\n2 Luganda" {
		t.Fatalf("END rendering = %q", rec.Body.String())
	}
}

// A wrong path token looks like a missing route, revealing nothing.
func TestATBadTokenIs404(t *testing.T) {
	p := &fakePort{}
	if rec := post(newHandler(t, p), "/v1/ussd/at/wrong", form("")); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(p.got) != 0 {
		t.Fatal("request reached intake")
	}
}

func TestATUnknownServiceCodeIs403(t *testing.T) {
	f := form("")
	f.Set("serviceCode", "*999#")
	if rec := post(newHandler(t, &fakePort{}), "/v1/ussd/at/"+token, f); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestATOversizeBodyRejected(t *testing.T) {
	f := form(strings.Repeat("1*", 6000))
	if rec := post(newHandler(t, &fakePort{}), "/v1/ussd/at/"+token, f); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestATRejectsWrongMethodAndContentType(t *testing.T) {
	h := newHandler(t, &fakePort{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ussd/at/"+token, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/ussd/at/"+token, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("JSON status = %d, want 415", rec.Code)
	}
}

// Fail safe to a human: an internal error still answers the caller, ending
// the session with the free voice line.
func TestATInternalErrorGivesVoiceLine(t *testing.T) {
	p := &fakePort{err: errors.New("db down")}
	rec := post(newHandler(t, p), "/v1/ussd/at/"+token, form(""))
	if rec.Code != http.StatusOK || rec.Body.String() != "END Sorry. Call 0800 100 200 free." {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
}

func TestATMissingSessionIDIs400(t *testing.T) {
	f := form("")
	f.Del("sessionId")
	if rec := post(newHandler(t, &fakePort{}), "/v1/ussd/at/"+token, f); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func FuzzATForm(f *testing.F) {
	f.Add("sessionId=a&serviceCode=%2A384%2A1%23&phoneNumber=%2B256772123456&text=1%2A2")
	f.Add("text=%ZZ")
	f.Add("")
	h, _ := NewUSSDHandler(USSDConfig{Provider: "at", PathToken: token, ServiceCodes: []string{"*384*1#"}, MaxBody: 8 << 10, ErrorText: "x"},
		&fakePort{screen: intake.Screen{Text: "ok"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/ussd/at/"+token, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case 200, 400, 403, 413:
		default:
			t.Fatalf("unexpected status %d for %q", rec.Code, body)
		}
	})
}
