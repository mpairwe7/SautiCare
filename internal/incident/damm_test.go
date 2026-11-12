package incident

import (
	"math/rand/v2"
	"testing"
)

// The canonical example from the Damm algorithm's published description:
// the check digit for 572 is 4.
func TestDammWikipediaVector(t *testing.T) {
	got, err := DammCheckDigit("572")
	if err != nil {
		t.Fatalf("DammCheckDigit: %v", err)
	}
	if got != '4' {
		t.Fatalf("check digit for 572 = %q, want '4'", got)
	}
	if !DammValid("5724") {
		t.Fatal("DammValid(5724) = false, want true")
	}
}

func randomBody(r *rand.Rand) string {
	b := make([]byte, 9)
	for i := range b {
		b[i] = byte('0' + r.IntN(10))
	}
	return string(b)
}

func withCheck(t *testing.T, body string) string {
	t.Helper()
	c, err := DammCheckDigit(body)
	if err != nil {
		t.Fatalf("DammCheckDigit(%q): %v", body, err)
	}
	return body + string(c)
}

// Every single-digit substitution anywhere in a 10-digit reference must be
// detected, because a mistyped digit would join a slip to the wrong incident.
func TestDammDetectsEverySingleDigitError(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		ref := withCheck(t, randomBody(r))
		for pos := range len(ref) {
			for d := byte('0'); d <= '9'; d++ {
				if d == ref[pos] {
					continue
				}
				mut := []byte(ref)
				mut[pos] = d
				if DammValid(string(mut)) {
					t.Fatalf("substitution at %d in %s -> %s not detected", pos, ref, mut)
				}
			}
		}
	}
}

// Every swap of two unequal neighbouring digits must be detected.
func TestDammDetectsEveryAdjacentTransposition(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range 2000 {
		ref := withCheck(t, randomBody(r))
		for pos := 0; pos+1 < len(ref); pos++ {
			if ref[pos] == ref[pos+1] {
				continue
			}
			mut := []byte(ref)
			mut[pos], mut[pos+1] = mut[pos+1], mut[pos]
			if DammValid(string(mut)) {
				t.Fatalf("transposition at %d in %s -> %s not detected", pos, ref, mut)
			}
		}
	}
}

func TestDammRejectsNonDigits(t *testing.T) {
	if _, err := DammCheckDigit("12a"); err == nil {
		t.Fatal("DammCheckDigit accepted a non-digit")
	}
	if DammValid("") || DammValid("57x4") {
		t.Fatal("DammValid accepted empty or non-digit input")
	}
}
