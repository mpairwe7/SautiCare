// Package pii holds the shared-kernel types for personal identifiers. Their
// whole purpose is to make leaking hard: every generic rendering path
// (fmt, %#v, JSON, slog) shows a mask, and the real value is only available
// through an explicitly named method that reviewers can grep for.
package pii

import (
	"errors"
	"log/slog"
	"strings"
)

// ErrInvalidPhone reports input that is not a usable telephone number.
var ErrInvalidPhone = errors.New("pii: invalid phone number")

// Phone is an E.164 telephone number. The zero value means "unknown"
// (for example a withheld number on the IVR line).
type Phone struct{ e164 string }

const (
	ugCountryCode = "256"
	ugNationalLen = 9 // digits after the trunk prefix 0, e.g. 772123456
	e164MinDigits = 8
	e164MaxDigits = 15
)

// ParsePhone normalises Ugandan local forms (0772123456, 256772123456,
// "+256 772 123 456") to +256772123456 and accepts any other E.164 number
// that starts with '+', because roaming and foreign SIMs can still call.
func ParsePhone(s string) (Phone, error) {
	var b strings.Builder
	plus := false
	for i, c := range strings.TrimSpace(s) {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '+' && i == 0:
			plus = true
		case c == ' ' || c == '-' || c == '(' || c == ')':
			continue
		default:
			return Phone{}, ErrInvalidPhone
		}
	}
	d := b.String()
	switch {
	case plus && strings.HasPrefix(d, "0"):
		return Phone{}, ErrInvalidPhone // country code 0 does not exist
	case strings.HasPrefix(d, ugCountryCode) && (plus || len(d) <= len(ugCountryCode)+1+ugNationalLen):
		// Uganda: drop a national trunk 0 written after the country code
		// (+256 0772…), so every form of one number normalises identically.
		national := strings.TrimPrefix(d[len(ugCountryCode):], "0")
		if len(national) != ugNationalLen {
			return Phone{}, ErrInvalidPhone
		}
		return Phone{e164: "+" + ugCountryCode + national}, nil
	case plus:
		if len(d) < e164MinDigits || len(d) > e164MaxDigits {
			return Phone{}, ErrInvalidPhone
		}
		return Phone{e164: "+" + d}, nil
	case strings.HasPrefix(d, "0") && len(d) == 1+ugNationalLen:
		return Phone{e164: "+" + ugCountryCode + d[1:]}, nil
	default:
		return Phone{}, ErrInvalidPhone
	}
}

// E164 reveals the full number. Only adapters that must dial or message the
// person (telephony, SMS, mobile money) and the audited console reveal call it.
func (p Phone) E164() string { return p.e164 }

// IsZero reports whether the number is unknown.
func (p Phone) IsZero() bool { return p.e164 == "" }

func (p Phone) mask() string {
	switch {
	case p.e164 == "":
		return "[no-phone]"
	case strings.HasPrefix(p.e164, "+"+ugCountryCode):
		return "+256•••••••••"
	default:
		return "+•••••••••••"
	}
}

// String implements fmt.Stringer with a mask.
func (p Phone) String() string { return p.mask() }

// GoString implements fmt.GoStringer with a mask (covers %#v).
func (p Phone) GoString() string { return "pii.Phone{" + p.mask() + "}" }

// LogValue implements slog.LogValuer with a mask.
func (p Phone) LogValue() slog.Value { return slog.StringValue(p.mask()) }

// MarshalJSON renders the mask, so a Phone accidentally placed in an event
// payload, outbox row or API response cannot carry the number.
func (p Phone) MarshalJSON() ([]byte, error) { return []byte(`"` + p.mask() + `"`), nil }

// PersonName is a responder's or staff member's name.
type PersonName struct{ v string }

// NewPersonName wraps a name.
func NewPersonName(s string) PersonName { return PersonName{v: strings.TrimSpace(s)} }

// Reveal returns the name. Only the audited console reveal and payment
// adapters (account-name checks) call it.
func (n PersonName) Reveal() string { return n.v }

func (n PersonName) String() string               { return "[name]" }
func (n PersonName) GoString() string             { return "pii.PersonName{[name]}" }
func (n PersonName) LogValue() slog.Value         { return slog.StringValue("[name]") }
func (n PersonName) MarshalJSON() ([]byte, error) { return []byte(`"[name]"`), nil }
