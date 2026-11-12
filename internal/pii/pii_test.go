package pii

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const digits = "772123456"

func mustPhone(t *testing.T, s string) Phone {
	t.Helper()
	p, err := ParsePhone(s)
	if err != nil {
		t.Fatalf("ParsePhone(%q): %v", s, err)
	}
	return p
}

// A Phone must never render its digits through any generic formatting path;
// only E164() reveals them (hard constraints 5 and 8, logging hygiene §13).
func TestPhoneMasksEverywhere(t *testing.T) {
	p := mustPhone(t, "0772123456")
	js, err := json.Marshal(struct{ P Phone }{p})
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("call", "phone", p, "nested", struct{ P Phone }{p})
	outputs := map[string]string{
		"Sprint":  fmt.Sprint(p),
		"%v":      fmt.Sprintf("%v", p),
		"%+v":     fmt.Sprintf("%+v", p),
		"%#v":     fmt.Sprintf("%#v", p),
		"%s":      fmt.Sprintf("%s", p),
		"json":    string(js),
		"slog":    logBuf.String(),
		"pointer": fmt.Sprintf("%v", &p),
	}
	for name, out := range outputs {
		if strings.Contains(out, digits) {
			t.Errorf("%s leaked digits: %s", name, out)
		}
	}
	if p.E164() != "+256772123456" {
		t.Fatalf("E164() = %q", p.E164())
	}
}

func TestParsePhoneUganda(t *testing.T) {
	for _, in := range []string{"0772123456", "256772123456", "+256 772 123 456", "+256-772-123456"} {
		if got := mustPhone(t, in).E164(); got != "+256772123456" {
			t.Errorf("ParsePhone(%q).E164() = %q, want +256772123456", in, got)
		}
	}
	// Roaming or foreign SIMs still dial in; any E.164 number is kept.
	if got := mustPhone(t, "+254712345678").E164(); got != "+254712345678" {
		t.Errorf("foreign E.164 = %q", got)
	}
	for _, bad := range []string{"", "12", "abc", "+", "07721234567890123", "0772 12345a"} {
		if _, err := ParsePhone(bad); err == nil {
			t.Errorf("ParsePhone(%q) accepted invalid input", bad)
		}
	}
}

func TestPhoneZero(t *testing.T) {
	var p Phone
	if !p.IsZero() {
		t.Fatal("zero Phone not IsZero")
	}
	if mustPhone(t, "0772123456").IsZero() {
		t.Fatal("parsed Phone reported IsZero")
	}
}

func TestPersonNameMasks(t *testing.T) {
	n := NewPersonName("Nakato Sarah")
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("x", "who", n)
	js, _ := json.Marshal(n)
	for _, out := range []string{fmt.Sprint(n), fmt.Sprintf("%#v", n), string(js), logBuf.String()} {
		if strings.Contains(out, "Nakato") {
			t.Errorf("name leaked: %s", out)
		}
	}
	if n.Reveal() != "Nakato Sarah" {
		t.Fatalf("Reveal() = %q", n.Reveal())
	}
}
