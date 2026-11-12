package triage

import (
	"crypto/mldsa"
	"testing"
)

// Whatever the inputs, Classify returns one of the three severities and
// never panics; anything outside the declared domain is red.
func FuzzClassifyNeverPanics(f *testing.F) {
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		f.Fatal(err)
	}
	sig, err := Sign(sk, "fuzz", []byte(rules))
	if err != nil {
		f.Fatal(err)
	}
	e, err := Load([]byte(rules), sig, map[string][]byte{"fuzz": sk.PublicKey().Bytes()})
	if err != nil {
		f.Fatal(err)
	}
	f.Add("yes", "trauma", "age_18_59", "no")
	f.Add("", "\x00", "unknown", "maybe")
	f.Fuzz(func(t *testing.T, danger, category, age, specific string) {
		r := e.Classify(Inputs{Danger: danger, Category: category, AgeBand: age, SpecificDanger: specific})
		if r.Severity != "red" && r.Severity != "yellow" && r.Severity != "green" {
			t.Fatalf("severity %q", r.Severity)
		}
	})
}
