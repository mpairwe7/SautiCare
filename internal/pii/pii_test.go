package pii

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
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
		"%s":      fmt.Sprintf("%s", p), //nolint:staticcheck // exercises the %s verb deliberately
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

// CodeRabbit PR #2: the same Ugandan caller must normalise to one value, or
// redial lookup hashes differ; country code 0 does not exist.
func TestParsePhoneNormalisesTrunkPrefixAndRejectsCountryCodeZero(t *testing.T) {
	for _, in := range []string{"+2560772123456", "+256 0772 123 456", "2560772123456"} {
		p, err := ParsePhone(in)
		if err != nil || p.E164() != "+256772123456" {
			t.Errorf("ParsePhone(%q) = %q, %v; want +256772123456", in, p.E164(), err)
		}
	}
	for _, bad := range []string{"+012345678", "+0772123456"} {
		if _, err := ParsePhone(bad); err == nil {
			t.Errorf("ParsePhone(%q) accepted country code 0", bad)
		}
	}
}

// canonicalInputs are TestParsePhoneUganda's accepted forms plus foreign
// numbers; FuzzParsePhoneCanonical seeds from them.
var canonicalInputs = []string{
	"0772123456", "256772123456", "+256 772 123 456", "+256-772-123456",
	"+2560772123456", "+256 0772 123 456", "2560772123456",
	"+254712345678", "+1 202 555 0143",
}

// notNumbers look like numbers but have no Ugandan national number: a
// national significant number never starts with the trunk prefix 0. Screen 8
// accepted them, and three later paths rejected the stored value (V-21).
var notNumbers = []string{"0000000000", "0012345678", "+2560077212345", "2560012345678"}

// A parsed number re-parses to itself, so every path that re-reads a stored
// number accepts what intake accepted (evaluation audit V-21, EA-7).
func TestParsePhoneIsCanonical(t *testing.T) {
	for _, in := range canonicalInputs {
		p := mustPhone(t, in)
		if q, err := ParsePhone(p.E164()); err != nil || q != p {
			t.Errorf("ParsePhone(%q) does not re-parse to itself (err %v)", in, err)
		}
	}
	for _, bad := range notNumbers {
		if _, err := ParsePhone(bad); !errors.Is(err, ErrInvalidPhone) {
			t.Errorf("ParsePhone(%q) = %v, want ErrInvalidPhone", bad, err)
		}
	}
}

// The same property over generated Ugandan and foreign forms: round trip
// (parse, render, parse gives the same Phone) and idempotence.
func TestParsePhoneRoundTripProperty(t *testing.T) {
	prefixes := []string{"", "+", "0", "00", "256", "+256", "+2560", "2560", "+256 0", "+1 ", "+44-"}
	gen := func(r *rand.Rand) string {
		b := []byte(prefixes[r.Intn(len(prefixes))])
		for range r.Intn(14) {
			b = append(b, "0123456789 -"[r.Intn(12)])
		}
		return string(b)
	}
	r := rand.New(rand.NewSource(1))
	accepted := 0
	for range 200000 {
		in := gen(r)
		p, err := ParsePhone(in)
		if err != nil {
			continue
		}
		accepted++
		q, err := ParsePhone(p.E164())
		if err != nil || q != p {
			t.Fatalf("ParsePhone(%q) does not re-parse to itself (err %v)", in, err)
		}
		if r2, _ := ParsePhone(q.E164()); r2 != q {
			t.Fatalf("ParsePhone is not idempotent from %q", in)
		}
	}
	if accepted < 1000 {
		t.Fatalf("the generator reached only %d valid numbers", accepted)
	}
}
