package africastalking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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

// Fail safe to a human (review finding I4): once the token and service code
// prove the request is ours, a malformed field still answers the caller with
// the free voice line instead of a bare 4xx the handset shows as an error.
func TestATMalformedFieldsGiveVoiceLine(t *testing.T) {
	cases := map[string]func(url.Values){
		"missing sessionId":  func(f url.Values) { f.Del("sessionId") },
		"line break in text": func(f url.Values) { f.Set("text", "1\n2") },
		"overlong text":      func(f url.Values) { f.Set("text", strings.Repeat("1", 1100)) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fakePort{}
			f := form("")
			mutate(f)
			rec := post(newHandler(t, p), "/v1/ussd/at/"+token, f)
			if rec.Code != http.StatusOK || rec.Body.String() != "END Sorry. Call 0800 100 200 free." {
				t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
			}
			if len(p.got) != 0 {
				t.Fatal("malformed request reached intake")
			}
		})
	}
}

func TestATBadFormEncodingGivesVoiceLine(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/ussd/at/"+token, strings.NewReader("text=%ZZ"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newHandler(t, &fakePort{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "END ") {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
}

// An unparseable or withheld number must not block an emergency report: the
// session proceeds with an unknown phone (dispatch proceeds on location).
func TestATUnparseablePhoneProceedsAsUnknown(t *testing.T) {
	for _, phone := range []string{"772123456", "", "unknown"} {
		p := &fakePort{screen: intake.Screen{Text: "ok", End: true}}
		f := form("")
		f.Set("phoneNumber", phone)
		rec := post(newHandler(t, p), "/v1/ussd/at/"+token, f)
		if rec.Code != http.StatusOK || rec.Body.String() != "END ok" {
			t.Fatalf("phone %q: response = %d %q", phone, rec.Code, rec.Body.String())
		}
		if len(p.got) != 1 || !p.got[0].Phone.IsZero() {
			t.Fatalf("phone %q: intake got %+v, want one call with an unknown phone", phone, p.got)
		}
	}
}

// logLines decodes every JSON log line written to buf.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// newLoggedHandler logs to buf without the process redactor: what is
// asserted is what the adapter itself writes.
func newLoggedHandler(t *testing.T, p *fakePort, buf *bytes.Buffer) http.Handler {
	t.Helper()
	h, err := NewUSSDHandler(USSDConfig{
		Provider: "at", PathToken: token, ServiceCodes: []string{"*384*1#"},
		MaxBody: 8 << 10, ErrorText: "Sorry. Call 0800 100 200 free.",
	}, p, slog.New(slog.NewJSONHandler(buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

var correlationValue = regexp.MustCompile(`^[0-9a-f]{16}$`)

// sessionId is free text from the aggregator: a name passes the printable
// check and the redactor does not mask names, so it is never logged. A keyed
// correlation value stands in, the same for every callback of one session
// (CodeRabbit PR #4).
func TestATLogsAKeyedCorrelationValueNeverTheSessionID(t *testing.T) {
	const nameLike = "Nakato Achieng Kampala"
	var buf bytes.Buffer
	p := &fakePort{screen: intake.Screen{Text: "ok"}}
	h := newLoggedHandler(t, p, &buf)
	session := func(id string, mutate func(url.Values)) {
		f := form("")
		f.Set("sessionId", id)
		if mutate != nil {
			mutate(f)
		}
		post(h, "/v1/ussd/at/"+token, f)
	}
	session(nameLike, nil)                                                    // the request line
	session(nameLike, func(f url.Values) { f.Set("phoneNumber", "unknown") }) // unparseable number
	session(nameLike, func(f url.Values) { f.Set("text", "1\n2") })           // answered with the voice line
	p.err = errors.New("db down")
	session(nameLike, nil) // intake failure
	p.err = nil
	session("ATUid_other", nil)

	if strings.Contains(buf.String(), "Nakato") || strings.Contains(buf.String(), "Achieng") ||
		strings.Contains(buf.String(), "ATUid_other") {
		t.Fatalf("raw sessionId in the logs:\n%s", buf.String())
	}
	var values []string
	for _, l := range logLines(t, &buf) {
		v, _ := l["session"].(string)
		if !correlationValue.MatchString(v) {
			t.Fatalf("session = %q, want 16 hex characters (line %v)", v, l)
		}
		values = append(values, v)
	}
	// request, number warning + request, voice line, intake failure + request, other request
	if len(values) != 7 {
		t.Fatalf("log lines = %d, want 7:\n%s", len(values), buf.String())
	}
	mine, other := values[:6], values[6]
	for _, v := range mine {
		if v != mine[0] {
			t.Fatalf("one session logged under different correlation values: %v", mine)
		}
	}
	if other == mine[0] {
		t.Fatal("two sessions share a correlation value")
	}
	if p.got[0].SessionID != nameLike {
		t.Fatalf("intake must still receive the provider's sessionId, got %q", p.got[0].SessionID)
	}
}

// networkCode is MCC+MNC: five or six digits. Anything else is dropped,
// treated as absent, and never logged (CodeRabbit PR #4).
func TestATNetworkCodeValidated(t *testing.T) {
	cases := map[string]string{"64110": "64110", "641100": "641100", "Nakato": "", "6411": "", "6411012345": "", "64 110": "", "": ""}
	for in, want := range cases {
		var buf bytes.Buffer
		p := &fakePort{screen: intake.Screen{Text: "ok"}}
		f := form("")
		f.Set("networkCode", in)
		post(newLoggedHandler(t, p, &buf), "/v1/ussd/at/"+token, f)
		if len(p.got) != 1 || p.got[0].NetworkCode != want {
			t.Fatalf("networkCode %q: intake got %+v, want %q", in, p.got, want)
		}
		lines := logLines(t, &buf)
		if got, _ := lines[len(lines)-1]["network"].(string); got != want {
			t.Fatalf("networkCode %q logged as %q, want %q", in, got, want)
		}
		if want == "" && in != "" && strings.Contains(buf.String(), in) {
			t.Fatalf("invalid networkCode %q logged raw:\n%s", in, buf.String())
		}
	}
}

// staticPort answers every request with the same screen and keeps nothing,
// so a fuzz run's memory does not grow with the inputs it accepts
// (CodeRabbit PR #4).
type staticPort struct{ screen intake.Screen }

func (p staticPort) HandleUSSD(context.Context, intake.USSDRequest) (intake.Screen, error) {
	return p.screen, nil
}

func FuzzATForm(f *testing.F) {
	f.Add("sessionId=a&serviceCode=%2A384%2A1%23&phoneNumber=%2B256772123456&text=1%2A2")
	f.Add("text=%ZZ")
	f.Add("")
	h, err := NewUSSDHandler(USSDConfig{Provider: "at", PathToken: token, ServiceCodes: []string{"*384*1#"}, MaxBody: 8 << 10, ErrorText: "x"},
		staticPort{screen: intake.Screen{Text: "ok"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/ussd/at/"+token, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case 200, 403, 413:
		default:
			t.Fatalf("unexpected status %d for %q", rec.Code, body)
		}
	})
}
