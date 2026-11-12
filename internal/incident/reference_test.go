package incident

import (
	"bytes"
	"crypto/rand"
	"errors"
	"regexp"
	"testing"
)

var spokenRE = regexp.MustCompile(`^[0-9]{3} [0-9]{3} [0-9]{4}$`)

func TestNewReferenceIsTenDigitsAndValid(t *testing.T) {
	for range 500 {
		ref, err := NewReference(rand.Reader)
		if err != nil {
			t.Fatalf("NewReference: %v", err)
		}
		s := string(ref)
		if len(s) != 10 {
			t.Fatalf("len(%q) = %d, want 10", s, len(s))
		}
		if !DammValid(s) {
			t.Fatalf("NewReference produced %q with a bad check digit", s)
		}
		if !spokenRE.MatchString(ref.Spoken()) {
			t.Fatalf("Spoken() = %q, want ddd ddd dddd", ref.Spoken())
		}
		if ref.Last4() != s[6:] {
			t.Fatalf("Last4() = %q, want %q", ref.Last4(), s[6:])
		}
	}
}

// A short or failing entropy source must surface as an error, never as a
// reference built from partial randomness.
func TestNewReferenceFailsOnShortEntropy(t *testing.T) {
	if _, err := NewReference(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("NewReference succeeded with 3 bytes of entropy")
	}
}

func TestParseReferenceAcceptsSpokenForms(t *testing.T) {
	canonical := withCheck(t, "123456789")
	spoken := canonical[:3] + " " + canonical[3:6] + " " + canonical[6:]
	hyphen := canonical[:3] + "-" + canonical[3:6] + "-" + canonical[6:]
	for _, in := range []string{canonical, spoken, hyphen, " " + spoken + " "} {
		got, err := ParseReference(in)
		if err != nil {
			t.Fatalf("ParseReference(%q): %v", in, err)
		}
		if string(got) != canonical {
			t.Fatalf("ParseReference(%q) = %q, want %q", in, got, canonical)
		}
	}
}

func TestParseReferenceRejects(t *testing.T) {
	good := withCheck(t, "123456789")
	badCheck := good[:9] + string('0'+((good[9]-'0'+1)%10))
	cases := map[string]string{
		"wrong check digit": badCheck,
		"nine digits":       good[:9],
		"eleven digits":     good + "1",
		"letters":           "12345678ab",
		"empty":             "",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseReference(in); !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("ParseReference(%q) err = %v, want ErrInvalidReference", in, err)
			}
		})
	}
}

func FuzzParseReference(f *testing.F) {
	f.Add("123 456 7890")
	f.Add("5724")
	f.Add("")
	f.Add("000-000-0000")
	f.Fuzz(func(t *testing.T, s string) {
		ref, err := ParseReference(s)
		if err != nil {
			return
		}
		again, err := ParseReference(ref.Spoken())
		if err != nil || again != ref {
			t.Fatalf("round trip of %q via %q failed: %v", ref, ref.Spoken(), err)
		}
	})
}
