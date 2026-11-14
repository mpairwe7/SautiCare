package pii

import "testing"

// FuzzParsePhoneCanonical: whenever ParsePhone accepts an input, the number
// it returns re-parses to itself (evaluation audit V-21).
func FuzzParsePhoneCanonical(f *testing.F) {
	for _, s := range append(append([]string{}, canonicalInputs...), notNumbers...) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParsePhone(s)
		if err != nil {
			return
		}
		q, err := ParsePhone(p.E164())
		if err != nil || q != p {
			t.Fatalf("ParsePhone(%q) does not re-parse to itself (err %v)", s, err)
		}
	})
}
