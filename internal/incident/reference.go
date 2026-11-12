package incident

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Reference is the incident number people read aloud, write on the Tool 3
// handover slip and key into a phone: 9 random digits plus a Damm check digit,
// spoken in groups of 3-3-4. It carries no personal information (ISP §5.2)
// and is separate from the uuidv7 primary key. Canonical form: 10 digits.
type Reference string

// ErrInvalidReference reports a value that is not a well-formed reference.
var ErrInvalidReference = errors.New("incident: invalid reference")

const (
	referenceBodyLen = 9
	referenceLen     = referenceBodyLen + 1
)

// NewReference draws 9 uniformly random digits from r (crypto/rand.Reader in
// production) and appends the Damm check digit. Bytes >= 250 are rejected so
// that byte%10 is uniform.
func NewReference(r io.Reader) (Reference, error) {
	body := make([]byte, 0, referenceLen)
	buf := make([]byte, 16)
	for len(body) < referenceBodyLen {
		n, err := io.ReadFull(r, buf)
		if err != nil {
			return "", fmt.Errorf("incident: reading entropy for reference: %w", err)
		}
		for _, b := range buf[:n] {
			if b >= 250 {
				continue
			}
			body = append(body, '0'+b%10)
			if len(body) == referenceBodyLen {
				break
			}
		}
	}
	check, err := DammCheckDigit(string(body))
	if err != nil {
		return "", err
	}
	return Reference(append(body, check)), nil
}

// ParseReference accepts the canonical 10 digits or a spoken/written form
// with spaces or hyphens ("123 456 7890", "123-456-7890") and validates the
// check digit.
func ParseReference(s string) (Reference, error) {
	var b strings.Builder
	for _, c := range strings.TrimSpace(s) {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ' || c == '-':
			continue
		default:
			return "", fmt.Errorf("%w: unexpected character", ErrInvalidReference)
		}
	}
	digits := b.String()
	if len(digits) != referenceLen || !DammValid(digits) {
		return "", ErrInvalidReference
	}
	return Reference(digits), nil
}

// Spoken returns the 3-3-4 grouping read aloud and printed on SMS: "123 456 7890".
func (r Reference) Spoken() string {
	s := string(r)
	if len(s) != referenceLen {
		return s
	}
	return s[:3] + " " + s[3:6] + " " + s[6:]
}

// Last4 returns the final four digits, used by the masked bridge to pick
// between several active assignments (Build Prompt §10.5).
func (r Reference) Last4() string {
	s := string(r)
	if len(s) < 4 {
		return s
	}
	return s[len(s)-4:]
}
