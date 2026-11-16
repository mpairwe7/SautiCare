//go:build integration

package main

import (
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A pregnancy emergency, the VHT and the rider told about each other, over
// HTTP against the real services (a demo preview, share.go):
//   - Transporter 1 accepts, then VHT 1 on a basic phone. Nothing shared
//     within the grace, so VHT 1 is told who is coming and to dial *384*3#.
//   - VHT 1 answers the menu: facility, this number, ward, landmark. The rider
//     gets the VHT's place, distance, facility and number; the page sees the
//     number masked.
//   - The basic phones record the arrival, the departure and the handover on
//     the same menu; the incident closes.
//   - A second call: VHT 4's smartphone shares before Transporter 3 accepts;
//     once they do, both are told at once.
func TestDemoVHTAndRiderAreToldAboutEachOther(t *testing.T) {
	pg := pgBin(t)
	bin := buildDemo(t)
	r := startRunner(t, filepath.Join(bin, "demo"), "--pg-bin", pg, "--data", t.TempDir(), "--config-dir", "../../config", "--rules-root", "../..")
	c := client{t: t, base: r.base}
	eventually(t, "the demo to start", 2*time.Minute, func() bool { p, _ := demoPhase(t, c.base); return p == phaseReady })

	refOf := regexp.MustCompile(`[0-9]{3} [0-9]{3} [0-9]{4}`)
	type incidentView struct {
		ID, Reference, State string
	}
	type shareJSON struct {
		Incident, VHT, Landmark, Facility, Number, Via string
		Told                                           bool
	}
	board := func() ([]incidentView, []shareJSON) {
		var b struct {
			Incidents []incidentView
			Shares    []shareJSON
		}
		c.do(http.MethodGet, "/api/board", nil, &b)
		return b.Incidents, b.Shares
	}
	idOf := func(ref string) string {
		ins, _ := board()
		for _, in := range ins {
			if in.Reference == ref {
				return in.ID
			}
		}
		return ""
	}
	inbox := func(phone string) []string {
		var out map[string][]struct{ Text string }
		c.do(http.MethodGet, "/api/sms", nil, &out)
		var s []string
		for _, m := range out[phone] {
			s = append(s, m.Text)
		}
		return s
	}
	got := func(phone, part string) bool {
		for _, m := range inbox(phone) {
			if strings.Contains(m, part) {
				return true
			}
		}
		return false
	}
	accept := func(phone, ref string) {
		eventually(t, phone+"'s alert for "+ref, 30*time.Second, func() bool { return got(phone, ref+": RED") })
		menu := c.dialAndWalk(phone, responderCode)
		if !strings.Contains(menu.Text, ref) {
			t.Fatalf("%s menu %q", phone, menu.Text)
		}
		var sc screen
		c.do(http.MethodPost, "/api/ussd", map[string]string{"session": menu.Session, "input": "1"}, &sc)
		if !sc.End || !strings.Contains(sc.Text, "Accepted") {
			t.Fatalf("%s accept: %+v", phone, sc)
		}
	}

	// A red pregnancy emergency at Test School 1.
	done := c.dialAndWalk("caller-a", callerCode, "1", "1", "2", "3", "2", "1", "3", "1")
	ref := refOf.FindString(done.Text)
	if !done.End || ref == "" {
		t.Fatalf("closing screen %+v", done)
	}
	if sc := c.dialAndWalk("vht-1", incidentCode); !sc.End || !strings.Contains(sc.Text, "no open incident") {
		t.Fatalf("the menu before accepting: %+v", sc)
	}
	accept("transporter-1", ref)
	accept("vht-1", ref)
	eventually(t, "VHT 1's prompt to dial "+incidentCode, 30*time.Second, func() bool {
		return got("vht-1", "Transporter 1 is coming to help. Call +256•••••••••. Dial *384*3# to send them where you are.")
	})
	if got("transporter-1", "VHT 1 is near") {
		t.Fatal("the rider was told before VHT 1 shared")
	}

	// VHT 1 answers the menu, with a wrong key on the way.
	root := c.dialAndWalk("vht-1", incidentCode)
	if root.End || !strings.Contains(root.Text, "My incident "+ref) || !strings.Contains(root.Text, "1 Send the transporter where I am\n2 I have arrived") {
		t.Fatalf("VHT 1's menu %q", root.Text)
	}
	var sc screen
	for _, step := range []struct{ in, want string }{
		{"1", "Which health facility do you work at?\n1 Test Health Centre 1"},
		{"9", "Choose from the list.\nPENDING-RESPONDER Which health facility"},
		{"1", "Is this phone's number the one to call you on?"},
		{"1", "Which ward are you in?"},
		{"1", "Nearest place to you?\n1 Test Market 1"},
		{"4", "Sent to Transporter 1: you are near Test Church 1, Test Ward 1."},
	} {
		c.do(http.MethodPost, "/api/ussd", map[string]string{"session": root.Session, "input": step.in}, &sc)
		if !strings.Contains(sc.Text, step.want) {
			t.Fatalf("after %s: %q, want %q", step.in, sc.Text, step.want)
		}
	}
	if !sc.End {
		t.Fatalf("the share did not end the session: %+v", sc)
	}
	eventually(t, "the rider's SMS", 30*time.Second, func() bool {
		return got("transporter-1", ref+": VHT 1 is near Test Church 1, Test Ward 1, 0.3 km from the patient. Works at Test Health Centre 1. Call +256•••••••••.")
	})
	for _, m := range append(inbox("transporter-1"), inbox("vht-1")...) {
		if strings.Contains(m, "772000") {
			t.Fatalf("an SMS reached the page unmasked: %q", m)
		}
	}
	if _, shares := board(); len(shares) != 1 || shares[0].VHT != "VHT 1" || shares[0].Via != viaUSSD || !shares[0].Told || shares[0].Number != "+256•••••••••" {
		t.Fatalf("shares %+v", shares)
	}

	// The basic phones complete the job on the menu.
	walk := func(phone, want string, inputs ...string) {
		t.Helper()
		if sc := c.dialAndWalk(phone, incidentCode, inputs...); !sc.End || !strings.Contains(sc.Text, want) {
			t.Fatalf("%s %v: %+v, want %q", phone, inputs, sc, want)
		}
	}
	walk("vht-1", "Arrival recorded for "+ref, "2")
	walk("transporter-1", "Arrival recorded for "+ref, "1")
	walk("transporter-1", ref+": on the way to the facility.", "1")
	walk("transporter-1", "Handed over at Test Health Centre 1.", "1", "1")
	eventually(t, "the incident to close", 30*time.Second, func() bool {
		ins, _ := board()
		for _, in := range ins {
			if in.Reference == ref {
				return in.State == "closed"
			}
		}
		return false
	})
	walk("vht-1", "no open incident")

	// Test Ward 2: VHT 4's smartphone shares before Transporter 3 accepts.
	done = c.dialAndWalk("caller-b", callerCode, "1", "1", "2", "3", "2", "2", "1", "1")
	ref2 := refOf.FindString(done.Text)
	if ref2 == "" {
		t.Fatalf("second call %+v", done)
	}
	accept("vht-4", ref2)
	id2 := idOf(ref2)
	var out struct{ Landmark, Transporter, Error string }
	if code := c.do(http.MethodPost, "/api/share", map[string]any{"phone": "transporter-3", "incident": id2, "facility": "hc_test_1", "lat": 0.3861, "lon": 32.5611}, &out); code != http.StatusBadRequest {
		t.Fatalf("a transporter's share: %d %+v", code, out)
	}
	if code := c.do(http.MethodPost, "/api/share", map[string]any{"phone": "vht-4", "incident": id2, "facility": "hc_test_1", "lat": 0.3866, "lon": 32.5609}, &out); code != http.StatusOK || out.Landmark != "Test Clinic 2" || out.Transporter != "" {
		t.Fatalf("VHT 4's share: %d %+v", code, out)
	}
	accept("transporter-3", ref2)
	eventually(t, "both told at once", 30*time.Second, func() bool {
		return got("transporter-3", ref2+": VHT 4 is near Test Clinic 2, Test Ward 2") && got("vht-4", ref2+": Transporter 3 is coming to help. Call +256•••••••••. They have your place and number.")
	})
	if got("vht-4", "Dial *384*3#") {
		t.Fatal("a VHT who had shared was still told to dial the menu")
	}
}
