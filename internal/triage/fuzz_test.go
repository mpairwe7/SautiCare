package triage

import (
	"crypto/mldsa"
	"slices"
	"testing"
)

// Whatever the inputs, Classify never panics; an input outside the declared
// domain is red with a rule error, and inputs inside it are never a rule
// error (evaluation audit V-04, ES-11).
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
	f.Add("no", "trauma", "age_18_59", "maybe")
	f.Fuzz(func(t *testing.T, danger, category, age, specific string) {
		in := Inputs{Danger: danger, Category: category, AgeBand: age, SpecificDanger: specific}
		r := e.Classify(in)
		inDomain := true
		for i, name := range inputNames {
			inDomain = inDomain && slices.Contains(e.Values(name), in.values()[i])
		}
		switch {
		case !inDomain && (r.Severity != Red || !r.RuleError):
			t.Fatalf("%+v is outside the domain: %+v, want red with a rule error", in, r)
		case inDomain && (r.RuleError || (r.Severity != Red && r.Severity != Yellow && r.Severity != Green)):
			t.Fatalf("%+v is inside the domain: %+v", in, r)
		}
	})
}
