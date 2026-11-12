package responder

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func ready() Responder {
	cert := now.Add(-24 * time.Hour)
	ins := now.Add(30 * 24 * time.Hour)
	return Responder{Role: RoleTransporter, Status: StatusActive, CertifiedAt: &cert, InsuredUntil: &ins, KitOK: true}
}

// Build Prompt §10.4: eligible responders are certified, insured and kit OK
// (the three gates); a suspended or retired responder is never eligible.
func TestGatesRequireCertifiedInsuredKitAndActive(t *testing.T) {
	if err := ready().Gates(now); err != nil {
		t.Fatalf("ready responder: %v", err)
	}
	for name, mut := range map[string]func(*Responder){
		"never certified":    func(r *Responder) { r.CertifiedAt = nil },
		"certified tomorrow": func(r *Responder) { c := now.Add(24 * time.Hour); r.CertifiedAt = &c },
		"never insured":      func(r *Responder) { r.InsuredUntil = nil },
		"insurance lapsed":   func(r *Responder) { i := now.Add(-time.Minute); r.InsuredUntil = &i },
		"insured until now":  func(r *Responder) { i := now; r.InsuredUntil = &i },
		"kit not ready":      func(r *Responder) { r.KitOK = false },
		"suspended":          func(r *Responder) { r.Status = StatusSuspended },
		"retired":            func(r *Responder) { r.Status = StatusRetired },
	} {
		r := ready()
		mut(&r)
		if err := r.Gates(now); err == nil {
			t.Errorf("%s: passed the gates", name)
		}
	}
}

func TestAvailableAt(t *testing.T) {
	r := ready()
	if r.AvailableAt(now) {
		t.Fatal("available without a period")
	}
	r.Available = &Period{From: now.Add(-time.Hour), Until: now.Add(7 * time.Hour)}
	if !r.AvailableAt(now) || r.AvailableAt(now.Add(7*time.Hour)) || r.AvailableAt(now.Add(-2*time.Hour)) {
		t.Fatal("period bounds wrong (half-open [from, until))")
	}
}

func TestParseRole(t *testing.T) {
	for in, want := range map[string]Role{"vht": RoleVHT, "transporter": RoleTransporter} {
		if got, err := ParseRole(in); err != nil || got != want {
			t.Errorf("ParseRole(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseRole("doctor"); err == nil {
		t.Fatal("unknown role accepted")
	}
}
