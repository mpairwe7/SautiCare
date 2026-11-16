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

// The steps after an acceptance, over HTTP (a demo preview of M3): a VHT and
// a transporter accept a red call; leaving before arriving is refused; the
// VHT attests arrival (T5), the transporter arrives, leaves with the patient
// (T6) and hands over at a facility (T7), which closes the incident; both
// responders are then alerted for the next call (F-75); the ledger verifies.
func TestDemoStepsAfterAcceptance(t *testing.T) {
	pg := pgBin(t)
	bin := buildDemo(t)
	r := startRunner(t, filepath.Join(bin, "demo"), "--pg-bin", pg, "--data", t.TempDir(), "--config-dir", "../../config", "--rules-root", "../..")
	c := client{t: t, base: r.base}
	eventually(t, "the demo to start", 2*time.Minute, func() bool { p, _ := demoPhase(t, c.base); return p == phaseReady })

	refOf := regexp.MustCompile(`[0-9]{3} [0-9]{3} [0-9]{4}`)
	// call reports a red emergency of category cat at Test Clinic 1. A call
	// of the same category nearby would merge into an open one (ADR-0022).
	call := func(phone, cat string) string {
		done := c.dialAndWalk(phone, callerCode, "1", "1", cat, "3", "2", "1", "2", "1")
		ref := refOf.FindString(done.Text)
		if !done.End || ref == "" {
			t.Fatalf("closing screen %+v", done)
		}
		return ref
	}
	type incidentView struct {
		ID, Reference, State string
		T                    map[string]string
		Alerts               []struct{ Responder, Outcome string }
	}
	find := func(ref string) incidentView {
		var b struct{ Incidents []incidentView }
		c.do(http.MethodGet, "/api/board", nil, &b)
		for _, in := range b.Incidents {
			if in.Reference == ref {
				return in
			}
		}
		return incidentView{}
	}
	accept := func(phone, ref string) {
		var inbox map[string][]struct{ Text string }
		eventually(t, phone+"'s alert for "+ref, 30*time.Second, func() bool {
			c.do(http.MethodGet, "/api/sms", nil, &inbox)
			for _, m := range inbox[phone] {
				if strings.Contains(m.Text, ref) && strings.Contains(m.Text, "accept") {
					return true
				}
			}
			return false
		})
		menu := c.dialAndWalk(phone, responderCode)
		if !strings.Contains(menu.Text, ref) || !strings.Contains(menu.Text, "1 Accept") {
			t.Fatalf("%s menu %q", phone, menu.Text)
		}
		var sc screen
		c.do(http.MethodPost, "/api/ussd", map[string]string{"session": menu.Session, "input": "1"}, &sc)
		if !sc.End || !strings.Contains(sc.Text, "Accepted") {
			t.Fatalf("%s accept: %+v", phone, sc)
		}
	}
	step := func(phone, id, s, facility string) (int, string) {
		var out struct{ State, Error string }
		code := c.do(http.MethodPost, "/api/progress", map[string]string{"phone": phone, "incident": id, "step": s, "facility": facility}, &out)
		return code, out.State + out.Error
	}

	ref := call("caller-a", "1")
	accept("vht-1", ref)
	accept("transporter-1", ref)
	var in incidentView
	eventually(t, "both acceptances and the stand-downs", 30*time.Second, func() bool {
		in = find(ref)
		pending := 0
		for _, a := range in.Alerts {
			if a.Outcome == "pending" {
				pending++
			}
		}
		return in.State == "assigned" && pending == 0
	})

	if code, msg := step("transporter-1", in.ID, stepDeparted, ""); code != http.StatusConflict {
		t.Fatalf("leaving before arriving: %d %s", code, msg)
	}
	if code, msg := step("vht-2", in.ID, stepArrived, ""); code != http.StatusConflict {
		t.Fatalf("arrival by a responder who did not accept: %d %s", code, msg)
	}
	if code, msg := step("vht-1", in.ID, stepDeparted, ""); code != http.StatusBadRequest {
		t.Fatalf("a VHT leaving with the patient: %d %s", code, msg)
	}
	for _, s := range []struct{ phone, step, facility, state string }{
		{"vht-1", stepArrived, "", "on_scene"},
		{"vht-1", stepArrived, "", "on_scene"}, // a double tap records once
		{"transporter-1", stepArrived, "", "on_scene"},
		{"transporter-1", stepDeparted, "", "transporting"},
		{"transporter-1", stepAtFacility, "hc_test_1", "closed"},
	} {
		if code, got := step(s.phone, in.ID, s.step, s.facility); code != http.StatusOK || got != s.state {
			t.Fatalf("%s %s: %d %q, want %q", s.phone, s.step, code, got, s.state)
		}
	}
	in = find(ref)
	if in.State != "closed" || in.T["T5"] == "" || in.T["T6"] == "" || in.T["T7"] == "" {
		t.Fatalf("after the handover: %+v", in)
	}

	// The next call alerts the same two again: closing released them.
	next := call("caller-b", "1")
	accept("vht-1", next)
	accept("transporter-1", next)

	// A coordinator closes a call whose alerts still wait for an answer: the
	// alerts in flight are stood down first, and their responders told.
	third := call("caller-a", "2")
	var open incidentView
	eventually(t, "alerts in flight for "+third, 30*time.Second, func() bool {
		open = find(third)
		for _, a := range open.Alerts {
			if a.Outcome == "pending" {
				return true
			}
		}
		return false
	})
	if code, got := step("", open.ID, stepClose, ""); code != http.StatusOK || got != "closed" {
		t.Fatalf("close with alerts in flight: %d %q", code, got)
	}
	open = find(third)
	stood := ""
	for _, a := range open.Alerts {
		if a.Outcome == "pending" {
			t.Fatalf("an alert still pending after the close: %+v", open.Alerts)
		}
		if a.Outcome == "stood_down" && stood == "" {
			stood = strings.ToLower(strings.ReplaceAll(a.Responder, " ", "-"))
		}
	}
	if stood == "" {
		t.Fatalf("no alert stood down by the close: %+v", open.Alerts)
	}
	var inbox map[string][]struct{ Text string }
	eventually(t, stood+"'s stand-down SMS", 30*time.Second, func() bool {
		c.do(http.MethodGet, "/api/sms", nil, &inbox)
		for _, m := range inbox[stood] {
			if strings.Contains(m.Text, third) && strings.Contains(m.Text, "no longer needed") {
				return true
			}
		}
		return false
	})
	if code, msg := step("", open.ID, stepClose, ""); code != http.StatusConflict {
		t.Fatalf("closing a closed incident: %d %s", code, msg)
	}

	var audit auditResult
	eventually(t, "a passing verification of the whole ledger", 30*time.Second, func() bool {
		if st := c.do(http.MethodPost, "/api/audit", map[string]string{}, &audit); st != http.StatusOK || !audit.OK {
			t.Fatalf("audit: %d %+v", st, audit)
		}
		return audit.Pending == 0 && audit.Entries == audit.Events && audit.Drift == 0
	})
}
