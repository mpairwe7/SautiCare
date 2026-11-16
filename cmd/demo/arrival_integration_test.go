//go:build integration

package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The caller asked whether help has arrived, over HTTP against the real
// services (a demo preview, arrival.go), at the 30-second pace:
//   - nobody has accepted: the caller's "no" on the USSD push reminds every
//     responder holding an alert and wakes dispatch;
//   - VHT 4 and Transporter 3 accept: the caller's "no" in the app tells
//     both the caller is still waiting;
//   - the caller's "yes" on the USSD push stops the asking and bounds the
//     arrival between the last "no" and the "yes".
func TestDemoTheCallerIsAskedWhetherHelpArrived(t *testing.T) {
	pg := pgBin(t)
	bin := buildDemo(t)
	r := startRunner(t, filepath.Join(bin, "demo"), "--pg-bin", pg, "--data", t.TempDir(), "--config-dir", "../../config", "--rules-root", "../..")
	c := client{t: t, base: r.base}
	eventually(t, "the demo to start", 2*time.Minute, func() bool { p, _ := demoPhase(t, c.base); return p == phaseReady })

	if code := c.do(http.MethodPost, "/api/arrival-pace", map[string]int{"seconds": 45}, nil); code != http.StatusBadRequest {
		t.Fatalf("a pace not offered: %d", code)
	}
	if code := c.do(http.MethodPost, "/api/arrival-pace", map[string]int{"seconds": 30}, nil); code != http.StatusOK {
		t.Fatalf("the 30-second pace: %d", code)
	}
	type ask struct {
		Answer, Via, Did string
	}
	type check struct {
		Incident, Phone    string
		Asks               []ask
		Open               *struct{ Session, Text string }
		ConfirmedAt, After *time.Time
	}
	var ref string
	state := func() (string, check) {
		var b struct {
			Incidents []struct{ ID, Reference string }
			Arrival   []check
		}
		c.do(http.MethodGet, "/api/board", nil, &b)
		for _, in := range b.Incidents {
			if in.Reference == ref {
				for _, a := range b.Arrival {
					if a.Incident == in.ID {
						return in.ID, a
					}
				}
				return in.ID, check{}
			}
		}
		return "", check{}
	}
	got := func(phone, part string) bool {
		var out map[string][]struct{ Text string }
		c.do(http.MethodGet, "/api/sms", nil, &out)
		for _, m := range out[phone] {
			if strings.Contains(m.Text, part) {
				return true
			}
		}
		return false
	}
	asked := func(n int) check {
		var a check
		eventually(t, "question "+string(rune('0'+n)), 70*time.Second, func() bool {
			_, a = state()
			return a.Open != nil && len(a.Asks) == n
		})
		if a.Phone != "caller-b" || !strings.Contains(a.Open.Text, ref+": has a health worker arrived yet?\n1 Yes\n2 No") {
			t.Fatalf("question %d: %+v", n, a)
		}
		return a
	}
	accept := func(phone string) {
		eventually(t, phone+"'s alert", 30*time.Second, func() bool { return got(phone, ref+": RED") })
		menu := c.dialAndWalk(phone, responderCode)
		var sc screen
		c.do(http.MethodPost, "/api/ussd", map[string]string{"session": menu.Session, "input": "1"}, &sc)
		if !strings.Contains(sc.Text, "Accepted "+ref) {
			t.Fatalf("%s: %+v", phone, sc)
		}
	}

	done := c.dialAndWalk("caller-b", callerCode, "1", "1", "2", "3", "2", "2", "1", "1")
	ref = refPattern.FindString(done.Text)
	if ref == "" {
		t.Fatalf("closing screen %+v", done)
	}

	// 1: nobody has accepted; "no" on the USSD push.
	a := asked(1)
	var sc screen
	c.do(http.MethodPost, "/api/ussd", map[string]string{"session": a.Open.Session, "input": "2"}, &sc)
	if !sc.End || !strings.Contains(sc.Text, "Sorry. Sauti is looking harder for help.") || !strings.Contains(sc.Text, "ask again in 30 s") {
		t.Fatalf("after no: %+v", sc)
	}
	eventually(t, "VHT 4's reminder", 10*time.Second, func() bool {
		return got("vht-4", ref+": the caller is still waiting and nobody has taken this. Dial *384*2# to accept.")
	})
	if _, a = state(); !strings.Contains(a.Asks[0].Did, "reminded VHT 4") || !strings.Contains(a.Asks[0].Did, "dispatch retried at once") || a.Asks[0].Via != viaUSSD {
		t.Fatalf("first answer: %+v", a.Asks[0])
	}

	// 2: both accepted; "no" in the app.
	accept("vht-4")
	accept("transporter-3")
	a = asked(2)
	id, _ := state()
	var out struct{ Text, Error string }
	if code := c.do(http.MethodPost, "/api/arrival", map[string]string{"phone": "caller-a", "incident": id, "answer": "no"}, &out); code != http.StatusConflict {
		t.Fatalf("another caller answered: %d %+v", code, out)
	}
	if code := c.do(http.MethodPost, "/api/arrival", map[string]string{"phone": "caller-b", "incident": id, "answer": "no"}, &out); code != http.StatusOK ||
		!strings.Contains(out.Text, "Sauti has told your helpers you are still waiting.") {
		t.Fatalf("the app's no: %d %+v", code, out)
	}
	for _, p := range []string{"vht-4", "transporter-3"} {
		if !got(p, ref+": the caller says help has not arrived yet and is still waiting.") {
			t.Fatalf("%s was not told the caller is waiting", p)
		}
	}
	c.do(http.MethodPost, "/api/ussd", map[string]string{"session": a.Open.Session, "input": "1"}, &sc)
	if !sc.End || !strings.Contains(sc.Text, "Session ended.") { // the app's answer closed the push session
		t.Fatalf("the push after the app answered: %+v", sc)
	}

	// 3: "yes" on the USSD push.
	a = asked(3)
	c.do(http.MethodPost, "/api/ussd", map[string]string{"session": a.Open.Session, "input": "1"}, &sc)
	if !sc.End || !strings.Contains(sc.Text, "Thank you. Sauti has noted that help arrived.") {
		t.Fatalf("after yes: %+v", sc)
	}
	_, a = state()
	if a.ConfirmedAt == nil || a.After == nil || !a.After.Before(*a.ConfirmedAt) || a.Open != nil || len(a.Asks) != 3 || a.Asks[2].Answer != answerYes {
		t.Fatalf("after yes: %+v", a)
	}
	time.Sleep(35 * time.Second)
	if _, a = state(); len(a.Asks) != 3 || a.Open != nil {
		t.Fatalf("asked again after yes: %+v", a)
	}
}
